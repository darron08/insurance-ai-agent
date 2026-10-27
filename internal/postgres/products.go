package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/indexing"
)

type ProductRepository struct{ DB *sql.DB }

func (r ProductRepository) ListProducts(ctx context.Context, profile indexing.Profile) ([]catalog.Product, error) {
	if r.DB == nil {
		return nil, errors.New("数据库连接为空")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// One statement uses one MVCC snapshot. Match M5's four profile checks for
	// every ready document, including versions with a mixture of profiles.
	rows, err := r.DB.QueryContext(ctx, `SELECT p.id, p.name, d.version, count(*),
		bool_and(d.embedding_profile IS NOT DISTINCT FROM $1
		AND d.embedding_base_url IS NOT DISTINCT FROM $2
		AND d.embedding_model IS NOT DISTINCT FROM $3
		AND d.embedding_dimensions IS NOT DISTINCT FROM $4)
		FROM insurance_products p JOIN documents d ON d.product_id=p.id
		WHERE d.processing_status='ready'
		GROUP BY p.id, p.name, d.version ORDER BY p.id COLLATE "C", d.version COLLATE "C"`,
		profile.Key(), profile.BaseURL, profile.Model, profile.Dimensions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	products := []catalog.Product{}
	for rows.Next() {
		var id string
		var name *string
		var version catalog.ProductVersion
		if err := rows.Scan(&id, &name, &version.Version, &version.DocumentCount, &version.Compatible); err != nil {
			return nil, err
		}
		if len(products) == 0 || products[len(products)-1].ProductID != id {
			products = append(products, catalog.Product{ProductID: id, Name: name, Versions: []catalog.ProductVersion{}})
		}
		last := &products[len(products)-1]
		last.Versions = append(last.Versions, version)
	}
	return products, rows.Err()
}
