package budget

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
)

// Price 是一次运行使用的不可变价目快照，金额单位均为货币的百万分之一。
type Price struct {
	Provider               string
	BaseURL                string
	Model                  string
	Currency               string
	Version                string
	Source                 string
	InputPerMillionMicros  int64
	OutputPerMillionMicros int64
}

func (p Price) CostMicros(input, output int64) (int64, error) {
	if input < 0 || output < 0 || p.InputPerMillionMicros < 0 || p.OutputPerMillionMicros < 0 {
		return 0, fmt.Errorf("用量或价格不能为负数")
	}
	total := new(big.Int).Mul(big.NewInt(input), big.NewInt(p.InputPerMillionMicros))
	total.Add(total, new(big.Int).Mul(big.NewInt(output), big.NewInt(p.OutputPerMillionMicros)))
	total.Add(total, big.NewInt(999999))
	total.Div(total, big.NewInt(1000000))
	if !total.IsInt64() {
		return 0, fmt.Errorf("费用计算溢出")
	}
	return total.Int64(), nil
}

// ParseMicros 将非负十进制货币金额转换为微单位，不使用浮点数。
func ParseMicros(value string) (int64, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || (len(parts) == 2 && (len(parts[1]) == 0 || len(parts[1]) > 6)) {
		return 0, fmt.Errorf("金额格式无效: %q", value)
	}
	for _, part := range parts {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return 0, fmt.Errorf("金额格式无效: %q", value)
			}
		}
	}
	whole := new(big.Int)
	if _, ok := whole.SetString(parts[0], 10); !ok {
		return 0, fmt.Errorf("金额格式无效: %q", value)
	}
	whole.Mul(whole, big.NewInt(1000000))
	if len(parts) == 2 {
		fraction := new(big.Int)
		fraction.SetString(parts[1]+strings.Repeat("0", 6-len(parts[1])), 10)
		whole.Add(whole, fraction)
	}
	if !whole.IsInt64() {
		return 0, fmt.Errorf("金额超出范围")
	}
	return whole.Int64(), nil
}

func FormatMicros(amount int64) string {
	return fmt.Sprintf("%d.%06d", amount/1000000, amount%1000000)
}

type priceFile struct {
	Version string `json:"version"`
	Rates   []struct {
		Provider         string `json:"provider"`
		BaseURL          string `json:"base_url"`
		Model            string `json:"model"`
		Currency         string `json:"currency"`
		Source           string `json:"source"`
		InputPerMillion  string `json:"input_per_million"`
		OutputPerMillion string `json:"output_per_million"`
	} `json:"rates"`
}

// LoadPrice 只选择与运行连接完全匹配的价格，不猜测供应商价目。
func LoadPrice(path, provider, baseURL, model string) (*Price, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取价目表失败: %w", err)
	}
	var file priceFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("解析价目表失败: %w", err)
	}
	if file.Version == "" {
		return nil, fmt.Errorf("价目表缺少版本")
	}
	var matched *Price
	for _, rate := range file.Rates {
		if rate.Provider != provider || strings.TrimRight(rate.BaseURL, "/") != strings.TrimRight(baseURL, "/") || rate.Model != model {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("价目表存在重复的 Provider、地址和模型组合")
		}
		if rate.Currency == "" {
			return nil, fmt.Errorf("价目表缺少币种")
		}
		input, err := ParseMicros(rate.InputPerMillion)
		if err != nil {
			return nil, err
		}
		output, err := ParseMicros(rate.OutputPerMillion)
		if err != nil {
			return nil, err
		}
		matched = &Price{Provider: provider, BaseURL: baseURL, Model: model, Currency: rate.Currency, Version: file.Version, Source: rate.Source,
			InputPerMillionMicros: input, OutputPerMillionMicros: output}
	}
	return matched, nil
}
