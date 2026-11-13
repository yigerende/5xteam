package sub2

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func (c *Client) SyncSpaceWorkflow(ctx context.Context, settings model.Sub2Settings, password string, id int64, marker model.SIWCWorkflowMarker) error {
	raw, err := c.doJSON(ctx, settings, password, http.MethodPut, fmt.Sprintf("/api/v1/admin/accounts/%d/space-workflow", id), marker, nil)
	if err != nil {
		return err
	}
	var receipt struct {
		Revision int64 `json:"revision"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Revision < marker.Revision {
		return fmt.Errorf("SIWC 标记确认版本无效")
	}
	return nil
}
