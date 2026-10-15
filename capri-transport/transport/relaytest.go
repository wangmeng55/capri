package transport

import (
	"context"
	"encoding/json"
)

// RelayForTest drives one relayed HTTP request through the in-process handler
// path exactly as the hub would (handleRelay -> respond) and returns the
// resulting status and body.
//
// It is exported solely so a capri-host integration test can compare the
// in-process path against the loopback path across the *real* server auth
// chain (withAuth/withCORS). It is not used in production. Call it only
// sequentially: it swaps the request-frame sink for the duration.
func (c *Client) RelayForTest(ctx context.Context, method, path string, body []byte) (status int, respBody []byte) {
	saved := c.reqCh
	c.reqCh = make(chan reqFrame, 16)
	defer func() { c.reqCh = saved }()

	var raw json.RawMessage
	if len(body) > 0 {
		raw = json.RawMessage(body)
	}
	c.handleRelay(ctx, "relaytest", c.cfg.HostID, method, path, raw)

	f := <-c.reqCh
	var frame struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}
	_ = json.Unmarshal(f.payload, &frame)
	return frame.Status, frame.Body
}
