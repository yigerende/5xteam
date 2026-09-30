package gptpay

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type CatalogCountry struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

type CatalogProduct struct {
	Code        string `json:"code"`
	Label       string `json:"label"`
	CreditPrice *int64 `json:"creditPrice,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type Catalog struct {
	Countries           []CatalogCountry  `json:"countries"`
	Currencies          map[string]string `json:"currencies"`
	CreditPrices        map[string]int64  `json:"creditPrices"`
	Products            []CatalogProduct  `json:"products"`
	ProductAvailability map[string]bool   `json:"productAvailability"`
}

// The public catalog lives outside /api/v1 and never receives account keys.
func (c *Client) Catalog(ctx context.Context, base string) (Catalog, error) {
	var v Catalog
	cfg, err := NormalizeSettings(Settings{Provider: CMSNav, URL: base})
	if err != nil {
		return v, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err = c.cmsnavCall(ctx, strings.TrimSuffix(cfg.URL, "/api/v1"), "", "GET", "/api/catalog", "", nil, &v)
	if err != nil {
		return v, err
	}
	if len(v.Countries) == 0 || (len(v.CreditPrices) == 0 && len(v.Products) == 0) {
		return v, fmt.Errorf("供应商未返回国家或套餐目录，请稍后刷新")
	}
	for i := range v.Countries {
		if v.Countries[i].Currency == "" {
			v.Countries[i].Currency = v.Currencies[v.Countries[i].Code]
		}
	}
	return v, nil
}
