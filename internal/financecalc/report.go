// Package financecalc runs the existing decimal financial formulas in the
// already bundled Node runtime. It never opens a network connection or database.
package financecalc

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
)

//go:embed report.mjs
var script []byte

func Calculate(ctx context.Context, input any) (json.RawMessage, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "space-finance-*.mjs")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(script); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "node", "--max-old-space-size=256", file.Name())
	cmd.Stdin = bytes.NewReader(body)
	// Never put financial inputs or process stderr in API responses or logs.
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("成本统计计算失败，请稍后重试")
	}
	if !json.Valid(out) {
		return nil, errors.New("成本统计结果无效")
	}
	return json.RawMessage(out), nil
}
