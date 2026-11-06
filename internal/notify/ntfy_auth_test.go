package notify

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/elsbrock/plundrio/internal/config"
	appLog "github.com/elsbrock/plundrio/internal/log"
)

type captureTransport func(*http.Request) (*http.Response, error)

func (f captureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func captureLogs(t *testing.T, send func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	appLog.SetLevel(appLog.LevelDebug)
	defer func() { os.Stdout = old; appLog.SetLevel(appLog.LevelInfo) }()
	sendErr := send()
	writer.Close()
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(data), sendErr
}

func TestNtfyBearerHeaderAndAnonymousCompatibility(t *testing.T) {
	for _, token := range []string{"", "synthetic-private-token"} {
		t.Run(map[bool]string{true: "authenticated", false: "anonymous"}[token != ""], func(t *testing.T) {
			n := NewNotifier(&config.Config{NtfyURL: "https://ntfy.invalid", NtfyTopic: "synthetic-topic", NtfyToken: token})
			calls := 0
			n.httpClient.Transport = captureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				want := ""
				if token != "" {
					want = "Bearer " + token
				}
				if r.Header.Get("Authorization") != want {
					t.Fatal("authorization header mismatch")
				}
				if r.Method != "POST" || r.URL.String() != "https://ntfy.invalid/synthetic-topic" {
					t.Fatal("request URL/method changed")
				}
				if token != "" && strings.Contains(r.URL.String(), token) {
					t.Fatal("token leaked in URL")
				}
				if r.Header.Get("Title") != "Synthetic title" || r.Header.Get("Priority") != "high" || r.Header.Get("Tags") != "white_check_mark,package,test" {
					t.Fatal("notification headers changed")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != "Synthetic body" {
					t.Fatal("body changed")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			})
			logs, err := captureLogs(t, func() error {
				return n.Send(Notification{Title: "Synthetic title", Message: "Synthetic body", Priority: PriorityHigh, Tags: []string{"test"}, Event: EventTransferComplete})
			})
			if err != nil || calls != 1 {
				t.Fatalf("send failed: %v, calls %d", err, calls)
			}
			if token != "" && strings.Contains(logs, token) {
				t.Fatal("token in logs")
			}
		})
	}
}

func TestNtfyErrorsDoNotDiscloseCredentialsOrTopic(t *testing.T) {
	const token = "synthetic-private-token"
	const topicURL = "https://ntfy.invalid/synthetic-private-topic"
	n := NewNotifier(&config.Config{NtfyURL: "https://ntfy.invalid", NtfyTopic: "synthetic-private-topic", NtfyToken: token})
	n.httpClient.Transport = captureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("missing token")
		}
		return nil, errors.New("deliberately sensitive transport failure " + token + " " + topicURL)
	})
	logs, err := captureLogs(t, func() error { return n.Send(Notification{}) })
	if err == nil {
		t.Fatal("missing error")
	}
	for _, secret := range []string{token, topicURL} {
		if strings.Contains(logs, secret) || strings.Contains(err.Error(), secret) {
			t.Fatal("transport secret disclosed")
		}
	}
	n.cfg.NtfyURL = "http://[invalid-" + token
	err = n.Send(Notification{})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("request construction secret disclosed")
	}
}

func TestNtfyRejectedAndDisabled(t *testing.T) {
	n := NewNotifier(&config.Config{NtfyURL: "https://ntfy.invalid", NtfyTopic: "synthetic-topic", NtfyToken: "synthetic-token"})
	calls := 0
	n.httpClient.Transport = captureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	if err := n.Send(Notification{}); err == nil || err.Error() != "ntfy returned status 401" {
		t.Fatal("401 not reported safely")
	}
	n.cfg.NtfyTopic = ""
	if err := n.Send(Notification{}); err != nil || calls != 1 {
		t.Fatal("disabled sender touched transport")
	}
}
