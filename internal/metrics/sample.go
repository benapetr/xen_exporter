package metrics

type Type string

const (
	Gauge   Type = "gauge"
	Counter Type = "counter"
)

type Sample struct {
	Name   string
	Help   string
	Type   Type
	Value  float64
	Labels map[string]string
}
