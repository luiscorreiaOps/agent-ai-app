package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
)

var thoughtSignatureCache sync.Map

type geminiThoughtRewriteTransport struct {
	base http.RoundTripper
}

func (t *geminiThoughtRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil && strings.Contains(req.URL.Host, "generativelanguage") {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			var payload map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &payload); err == nil {
				modified := false

				if messages, ok := payload["messages"].([]interface{}); ok {
					for _, msgObj := range messages {
						msg, _ := msgObj.(map[string]interface{})
						if msg["role"] == "assistant" {
							if toolCalls, ok := msg["tool_calls"].([]interface{}); ok {
								for _, tcObj := range toolCalls {
									tc, _ := tcObj.(map[string]interface{})
									if idStr, ok := tc["id"].(string); ok {
										if sig, ok := thoughtSignatureCache.Load(idStr); ok {
											tc["extra_content"] = map[string]interface{}{
												"google": map[string]interface{}{
													"thought_signature": sig.(string),
												},
											}
											modified = true
											log.Printf("Found cached thought_signature for %s", idStr)
										} else {
											log.Printf("No thought_signature found for %s", idStr)
										}
									}
								}
							}
						}
					}
				}

				if modified {
					if newBody, err := json.Marshal(payload); err == nil {
						req.Body = io.NopCloser(bytes.NewReader(newBody))
						req.ContentLength = int64(len(newBody))
					} else {
						log.Printf("Failed to inject Gemini thought_signature metadata: %v", err)
						req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
					}
				} else {
					req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				}
			} else {
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if resp.Body != nil && strings.Contains(req.URL.Host, "generativelanguage") {
		contentType := resp.Header.Get("Content-Type")
		if strings.Contains(contentType, "text/event-stream") {
			log.Printf("Wrapping response body in stream interceptor")
			resp.Body = &geminiStreamInterceptor{
				ReadCloser: resp.Body,
			}
		} else {
			log.Printf("Reading response body fully for non-stream thought_signature extraction")
			bodyBytes, readErr := io.ReadAll(resp.Body)
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("Failed to close Gemini response body after signature extraction: %v", closeErr)
			}
			if readErr == nil {
				var payload map[string]interface{}
				if err := json.Unmarshal(bodyBytes, &payload); err == nil {
					extractSignaturesFromPayload(payload)
				}
				resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			} else {
				resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}
	}

	return resp, nil
}

func extractSignaturesFromPayload(payload map[string]interface{}) {
	if choices, ok := payload["choices"].([]interface{}); ok {
		for _, choiceObj := range choices {
			choice, _ := choiceObj.(map[string]interface{})
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if toolCalls, ok := message["tool_calls"].([]interface{}); ok {
					for _, tcObj := range toolCalls {
						tc, _ := tcObj.(map[string]interface{})
						if idStr, ok := tc["id"].(string); ok {
							if extra, ok := tc["extra_content"].(map[string]interface{}); ok {
								if google, ok := extra["google"].(map[string]interface{}); ok {
									if sig, ok := google["thought_signature"].(string); ok {
										log.Printf("Intercepted thought_signature for %s in non-stream response", idStr)
										thoughtSignatureCache.Store(idStr, sig)
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

type geminiStreamInterceptor struct {
	io.ReadCloser
	buf []byte
}

func (i *geminiStreamInterceptor) Read(p []byte) (n int, err error) {
	n, err = i.ReadCloser.Read(p)
	if n > 0 {
		i.buf = append(i.buf, p[:n]...)

		for {
			idx := bytes.IndexByte(i.buf, '\n')
			if idx == -1 {
				break
			}
			line := i.buf[:idx]
			i.buf = i.buf[idx+1:]

			lineStr := strings.TrimSpace(string(line))
			if lineStr == "" {
				continue
			}

			var payload map[string]interface{}
			if strings.HasPrefix(lineStr, "data: ") {
				jsonStr := strings.TrimPrefix(lineStr, "data: ")
				if jsonStr != "[DONE]" {
					if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
						continue
					}
				}
			} else if strings.HasPrefix(lineStr, "{") {
				if err := json.Unmarshal([]byte(lineStr), &payload); err != nil {
					continue
				}
			}

			if payload != nil {
				if choices, ok := payload["choices"].([]interface{}); ok {
					for _, choiceObj := range choices {
						choice, _ := choiceObj.(map[string]interface{})

						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
								for _, tcObj := range toolCalls {
									tc, _ := tcObj.(map[string]interface{})
									if idStr, ok := tc["id"].(string); ok {
										if extra, ok := tc["extra_content"].(map[string]interface{}); ok {
											if google, ok := extra["google"].(map[string]interface{}); ok {
												if sig, ok := google["thought_signature"].(string); ok {
													log.Printf("Intercepted thought_signature for %s", idStr)
													thoughtSignatureCache.Store(idStr, sig)
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return n, err
}
