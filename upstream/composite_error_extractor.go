package upstream

import (
	"net/http"

	"github.com/erpc/erpc/common"
)

// CompositeJsonRpcErrorExtractor tries each registered architecture's extractor in order
// and returns the first non-nil result. This allows a single ClientRegistry to serve
// multiple architectures without knowing which one owns a given error.
type CompositeJsonRpcErrorExtractor struct {
	extractors []common.JsonRpcErrorExtractor
}

func NewCompositeJsonRpcErrorExtractor() *CompositeJsonRpcErrorExtractor {
	c := &CompositeJsonRpcErrorExtractor{}
	for _, h := range common.ArchitectureRegistry {
		c.extractors = append(c.extractors, h.NewJsonRpcErrorExtractor())
	}
	return c
}

func (c *CompositeJsonRpcErrorExtractor) Extract(resp *http.Response, nr *common.NormalizedResponse, jr *common.JsonRpcResponse, upstream common.Upstream) error {
	for _, e := range c.extractors {
		if extracted := e.Extract(resp, nr, jr, upstream); extracted != nil {
			return extracted
		}
	}
	return nil
}
