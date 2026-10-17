package handler

import (
	"fmt"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
	"github.com/choizeus02/paper-reader/backend/internal/service"
)

type pageResponse struct {
	PaperID   string             `json:"paper_id"`
	PageNo    int                `json:"page_no"`
	PageCount int                `json:"page_count"`
	Width     float64            `json:"width"`
	Height    float64            `json:"height"`
	ImageURL  string             `json:"image_url"`
	Blocks    []domain.Block     `json:"blocks"`
	Result    *domain.PageResult `json:"result"`
}

func newPageResponse(v service.PageView) pageResponse {
	blocks := v.Page.Blocks
	if blocks == nil {
		blocks = []domain.Block{}
	}
	return pageResponse{
		PaperID: v.Page.PaperID, PageNo: v.Page.PageNo, PageCount: v.PageCount,
		Width: v.Page.Width, Height: v.Page.Height,
		ImageURL: fmt.Sprintf("/api/papers/%s/pages/%d/image", v.Page.PaperID, v.Page.PageNo),
		Blocks:   blocks, Result: v.Result,
	}
}

type prefetchRequest struct {
	From  int `json:"from"`
	Count int `json:"count"`
}

type questionRequest struct {
	Anchor   domain.Anchor `json:"anchor"`
	Question string        `json:"question"`
}
