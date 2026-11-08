package quality

import (
	"fmt"
	"math/big"
	"strings"
)

type expression struct {
	op, text string
	args     []*expression
}
type parser struct {
	text       string
	pos, nodes int
}

func isDigit(b byte) bool     { return b >= '0' && b <= '9' }
func isNameStart(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_' }
func isName(b byte) bool      { return isNameStart(b) || isDigit(b) }
func (p *parser) space() {
	for p.pos < len(p.text) && (p.text[p.pos] == ' ' || p.text[p.pos] == '\t' || p.text[p.pos] == '\r' || p.text[p.pos] == '\n') {
		p.pos++
	}
}
func (p *parser) node(op, text string, args ...*expression) (*expression, error) {
	p.nodes++
	if p.nodes > MaxNodes {
		return nil, fmt.Errorf("表达式节点超过%d", MaxNodes)
	}
	return &expression{op, text, args}, nil
}
func parseExpression(text string) (*expression, error) {
	if len(text) > 8192 {
		return nil, fmt.Errorf("表达式过长")
	}
	p := &parser{text: text}
	e, err := p.parse(0, 0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.text) {
		return nil, fmt.Errorf("表达式位置%d含不支持的语法", p.pos)
	}
	return e, nil
}
func (p *parser) parse(minPrec, depth int) (*expression, error) {
	if depth >= MaxDepth {
		return nil, fmt.Errorf("表达式深度超过%d", MaxDepth)
	}
	p.space()
	if p.pos == len(p.text) {
		return nil, fmt.Errorf("表达式不完整")
	}
	var left *expression
	var err error
	c := p.text[p.pos]
	switch {
	case c == '+' || c == '-':
		p.pos++
		arg, e := p.parse(3, depth+1)
		if e != nil {
			return nil, e
		}
		left, err = p.node(string(c), "", arg)
	case c == '(':
		p.pos++
		left, err = p.parse(0, depth+1)
		if err != nil {
			return nil, err
		}
		p.space()
		if p.pos >= len(p.text) || p.text[p.pos] != ')' {
			return nil, fmt.Errorf("表达式缺少右括号")
		}
		p.pos++
	case isDigit(c) || c == '.':
		start := p.pos
		for p.pos < len(p.text) && (isDigit(p.text[p.pos]) || p.text[p.pos] == '.') {
			p.pos++
		}
		token := p.text[start:p.pos]
		if _, e := decimal(token); e != nil {
			return nil, e
		}
		left, err = p.node("literal", token)
	case isNameStart(c):
		start := p.pos
		for p.pos < len(p.text) && isName(p.text[p.pos]) {
			p.pos++
		}
		name := p.text[start:p.pos]
		p.space()
		if p.pos < len(p.text) && p.text[p.pos] == '(' {
			if name != "sum" {
				return nil, fmt.Errorf("仅支持sum加总，不支持函数%s", name)
			}
			p.pos++
			args := []*expression{}
			for {
				arg, e := p.parse(0, depth+1)
				if e != nil {
					return nil, e
				}
				args = append(args, arg)
				p.space()
				if p.pos >= len(p.text) {
					return nil, fmt.Errorf("sum缺少右括号")
				}
				next := p.text[p.pos]
				p.pos++
				if next == ')' {
					break
				}
				if next != ',' {
					return nil, fmt.Errorf("sum参数需要逗号分隔")
				}
				if len(args) >= MaxNodes {
					return nil, fmt.Errorf("sum参数过多")
				}
			}
			left, err = p.node("sum", "", args...)
		} else {
			left, err = p.node("variable", name)
		}
	default:
		return nil, fmt.Errorf("表达式位置%d含不支持的语法", p.pos)
	}
	if err != nil {
		return nil, err
	}
	for {
		p.space()
		if p.pos >= len(p.text) {
			break
		}
		op := p.text[p.pos]
		prec := 0
		if op == '+' || op == '-' {
			prec = 1
		}
		if op == '*' || op == '/' {
			prec = 2
		}
		if prec == 0 || prec < minPrec {
			break
		}
		p.pos++
		right, e := p.parse(prec+1, depth+1)
		if e != nil {
			return nil, e
		}
		left, err = p.node(string(op), "", left, right)
		if err != nil {
			return nil, err
		}
	}
	if expressionDepth(left) > MaxDepth {
		return nil, fmt.Errorf("表达式树深度超过%d", MaxDepth)
	}
	return left, nil
}
func expressionDepth(e *expression) int {
	n := 0
	for _, a := range e.args {
		n = max(n, expressionDepth(a))
	}
	return n + 1
}
func decimal(text string) (*big.Rat, error) {
	if len(text) == 0 || len(text) > MaxLiteral {
		return nil, fmt.Errorf("数字字面量须为1至%d字符", MaxLiteral)
	}
	start := 0
	if text[0] == '-' || text[0] == '+' {
		start++
	}
	digits, dots := 0, 0
	for i := start; i < len(text); i++ {
		if isDigit(text[i]) {
			digits++
		} else if text[i] == '.' {
			dots++
		} else {
			return nil, fmt.Errorf("仅支持十进制数字：%q", text)
		}
	}
	if digits == 0 || dots > 1 {
		return nil, fmt.Errorf("十进制数字无效：%q", text)
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, fmt.Errorf("十进制数字无效：%q", text)
	}
	return r, nil
}
func bounded(r *big.Rat) (*big.Rat, error) {
	if r.Num().BitLen() > MaxBits || r.Denom().BitLen() > MaxBits {
		return nil, fmt.Errorf("中间结果超过%d位", MaxBits)
	}
	return r, nil
}
func (e *expression) evaluate(values map[string]*big.Rat) (*big.Rat, error) {
	if e.op == "literal" {
		return decimal(e.text)
	}
	if e.op == "variable" {
		v := values[e.text]
		if v == nil {
			return nil, fmt.Errorf("变量%s未绑定", e.text)
		}
		return new(big.Rat).Set(v), nil
	}
	a, err := e.args[0].evaluate(values)
	if err != nil {
		return nil, err
	}
	if len(e.args) == 1 {
		if e.op == "-" {
			a.Neg(a)
		}
		return bounded(a)
	}
	for i, bexpr := range e.args[1:] {
		b, err := bexpr.evaluate(values)
		if err != nil {
			return nil, err
		}
		switch e.op {
		case "+", "sum":
			a.Add(a, b)
		case "-":
			a.Sub(a, b)
		case "*":
			a.Mul(a, b)
		case "/":
			if b.Sign() == 0 {
				return nil, fmt.Errorf("除数为零")
			}
			a.Quo(a, b)
		default:
			return nil, fmt.Errorf("不支持的算术操作%s/%d", e.op, i)
		}
		if _, err = bounded(a); err != nil {
			return nil, err
		}
	}
	return a, nil
}
func (e *expression) render() string {
	if e.op == "literal" || e.op == "variable" {
		return e.text
	}
	args := make([]string, len(e.args))
	for i, a := range e.args {
		args[i] = a.render()
	}
	if e.op == "sum" {
		return "sum(" + strings.Join(args, ", ") + ")"
	}
	if len(args) == 1 {
		return "(" + e.op + args[0] + ")"
	}
	return "(" + strings.Join(args, " "+e.op+" ") + ")"
}
func (e *expression) variables(out map[string]bool) {
	if e.op == "variable" {
		out[e.text] = true
	}
	for _, a := range e.args {
		a.variables(out)
	}
}
