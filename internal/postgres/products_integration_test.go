package postgres

import (
	"testing"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/indexing"
)

func TestM7ProductDirectory(t *testing.T) {
	db, ctx := indexTestDB(t)
	p, _ := indexing.NewProfile("", "")
	s := catalog.DirectoryService{Repository: ProductRepository{DB: db}, Profile: p}
	products, err := s.ListProducts(ctx)
	if err != nil || products == nil || len(products) != 0 {
		t.Fatalf("empty directory=%+v error=%v", products, err)
	}
	storeSearchDocument(t, db, ctx, "a-empty", "a", "", p, 0)
	storeSearchDocument(t, db, ctx, "a-v1", "a", "v1", p, 0)
	storeSearchDocument(t, db, ctx, "b-v1", "b", "v1", p, 0)
	other, _ := indexing.NewProfile("http://other.example/v1", "other-model")
	storeSearchDocument(t, db, ctx, "b-mixed", "b", "v1", other, 0)
	input := integrationDocument("not-ready")
	if _, err := (catalog.Service{Repository: Repository{DB: db}}).Store(ctx, input, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE insurance_products SET name='产品 A' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	products, err = s.ListProducts(ctx)
	if err != nil || len(products) != 2 {
		t.Fatalf("directory=%+v error=%v", products, err)
	}
	if products[0].ProductID != "a" || products[0].Name == nil || *products[0].Name != "产品 A" || len(products[0].Versions) != 2 || products[0].Versions[0].Version != "" || !products[0].Versions[0].Compatible {
		t.Fatalf("product a=%+v", products[0])
	}
	if products[1].Name != nil || products[1].Versions[0].DocumentCount != 2 || products[1].Versions[0].Compatible {
		t.Fatalf("mixed product=%+v", products[1])
	}
	db.Close()
	if _, err := s.ListProducts(ctx); err == nil {
		t.Fatal("closed database succeeded")
	}
}
