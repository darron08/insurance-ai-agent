package httpapi

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"insurance_agent_demo/internal/catalog"
	"insurance_agent_demo/internal/rag"
)

type ProductLister interface {
	ListProducts(context.Context) ([]catalog.Product, error)
}

func (a api) listProducts(c *gin.Context) {
	if a.products == nil {
		writeError(c, rag.ClassifyError(errors.New("未配置目录"), "catalog", ""))
		return
	}
	products, err := a.products.ListProducts(c.Request.Context())
	if err != nil {
		writeError(c, rag.ClassifyError(err, "catalog", ""))
		return
	}
	if products == nil {
		products = []catalog.Product{}
	}
	c.JSON(200, gin.H{"products": products})
}
