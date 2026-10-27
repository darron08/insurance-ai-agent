package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"insurance_agent_demo/internal/indexing"
)

type queryFunc func(context.Context, string) ([]float64, error)

func (f queryFunc) EmbedQuery(ctx context.Context, q string) ([]float64, error) { return f(ctx, q) }

type repositoryFunc func(context.Context, SearchParams) (Result, error)

func (f repositoryFunc) Search(ctx context.Context, p SearchParams) (Result, error) { return f(ctx, p) }

func queryVector() []float64 {
	v := make([]float64, indexing.Dimensions)
	v[0] = 1
	return v
}

func TestSearchPipelineAndVersionPresence(t *testing.T) {
	profile, _ := indexing.NewProfile("", "")
	empty := ""
	for _, version := range []*string{nil, &empty} {
		s := Service{Profile: profile,
			Embedder: queryFunc(func(_ context.Context, q string) ([]float64, error) {
				if q != "等待期？" {
					t.Fatalf("query=%q", q)
				}
				return queryVector(), nil
			}),
			Repository: repositoryFunc(func(_ context.Context, p SearchParams) (Result, error) {
				if (p.Request.Version == nil) != (version == nil) || p.Request.TopK != 5 || p.Request.Mode != ModeHNSW || p.Profile != profile || p.Vector[0] != 1 {
					t.Fatalf("params=%+v", p)
				}
				return Result{Version: ""}, nil
			}),
		}
		got, err := s.Search(context.Background(), Request{ProductID: "demo", Query: " 等待期？ ", Version: version})
		if err != nil || got.Query != "等待期？" || got.Model != profile.Model || got.EmbeddingProfile != profile.Key() || got.QueryTemplate != QueryTemplate || got.Hits == nil || got.TopK != 5 {
			t.Fatalf("result=%+v err=%v", got, err)
		}
		raw, _ := json.Marshal(got)
		if !strings.Contains(string(raw), `"hits":[]`) {
			t.Fatal(string(raw))
		}
	}
}

func TestInvalidSearchDoesNotReachIO(t *testing.T) {
	profile, _ := indexing.NewProfile("", "")
	space := " v1"
	valid := Request{ProductID: "demo", Query: "等待期？", TopK: 5, Mode: ModeExact}
	for _, name := range []string{"product", "product whitespace", "query", "version", "k negative", "k high", "mode", "profile", "dimensions", "dependencies"} {
		t.Run(name, func(t *testing.T) {
			req := valid
			s := Service{Profile: profile, Embedder: queryFunc(func(context.Context, string) ([]float64, error) { t.Fatal("model called"); return nil, nil }),
				Repository: repositoryFunc(func(context.Context, SearchParams) (Result, error) {
					t.Fatal("repository called")
					return Result{}, nil
				})}
			switch name {
			case "product":
				req.ProductID = ""
			case "product whitespace":
				req.ProductID = " demo"
			case "query":
				req.Query = " \n "
			case "version":
				req.Version = &space
			case "k negative":
				req.TopK = -1
			case "k high":
				req.TopK = 101
			case "mode":
				req.Mode = "unknown"
			case "profile":
				s.Profile.BaseURL = "bad"
			case "dimensions":
				s.Profile.Dimensions = 1536
			case "dependencies":
				s.Repository = nil
			}
			if _, err := s.Search(context.Background(), req); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestInvalidQueryVectorsAndFailures(t *testing.T) {
	profile, _ := indexing.NewProfile("", "")
	for _, name := range []string{"dimension", "zero", "nan", "inf", "overflow", "underflow", "model failure", "canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := errors.New("model failure")
			s := Service{Profile: profile, Repository: repositoryFunc(func(context.Context, SearchParams) (Result, error) {
				t.Fatal("repository called")
				return Result{}, nil
			}),
				Embedder: queryFunc(func(context.Context, string) ([]float64, error) {
					v := queryVector()
					switch name {
					case "dimension":
						v = []float64{1}
					case "zero":
						v[0] = 0
					case "nan":
						v[0] = math.NaN()
					case "inf":
						v[0] = math.Inf(1)
					case "overflow":
						v[0] = math.MaxFloat64
					case "underflow":
						v[0] = math.SmallestNonzeroFloat64
					case "model failure":
						return nil, upstream
					case "canceled":
						cancel()
					}
					return v, nil
				})}
			_, err := s.Search(ctx, Request{ProductID: "demo", Query: "问题"})
			if err == nil {
				t.Fatal("invalid vector accepted")
			}
			if name == "model failure" && !errors.Is(err, upstream) {
				t.Fatal(err)
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := Service{Profile: profile, Embedder: queryFunc(func(context.Context, string) ([]float64, error) { t.Fatal("canceled model called"); return nil, nil }), Repository: repositoryFunc(func(context.Context, SearchParams) (Result, error) {
		t.Fatal("canceled repository called")
		return Result{}, nil
	})}
	if _, err := s.Search(ctx, Request{ProductID: "demo", Query: "问题"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	for _, failure := range []error{ErrNoReadyDocuments, ErrProfileMismatch, &VersionRequiredError{Versions: []string{"", "v1"}}} {
		s.Embedder = queryFunc(func(context.Context, string) ([]float64, error) { return queryVector(), nil })
		s.Repository = repositoryFunc(func(context.Context, SearchParams) (Result, error) { return Result{}, failure })
		if _, err := s.Search(context.Background(), Request{ProductID: "demo", Query: "问题"}); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
}
