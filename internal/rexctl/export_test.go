package rexctl

// Writing reports whether c is in the middle of writing a request to its
// socket, for tests that block the writer.
func Writing(c *Client) bool { return c.writing.Load() }
