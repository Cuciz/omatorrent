package ipc

import "testing"

func TestParseFrameValid(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  Request
	}{
		{"hello", `{"type":"hello","protocol":1}`, Request{Type: "hello", Protocol: 1}},
		{"hello with whitespace", "{ \"type\" : \"hello\" , \"protocol\" : 1 }", Request{Type: "hello", Protocol: 1}},
		{"health", `{"type":"health","id":42}`, Request{Type: "health", ID: 42}},
		{"system.status", `{"type":"system.status","id":7}`, Request{Type: "system.status", ID: 7}},
		{"negative id", `{"type":"health","id":-3}`, Request{Type: "health", ID: -3}},
		{"unknown type type-only", `{"type":"wat"}`, Request{Type: "wat"}},
		{"protocol zero", `{"type":"hello","protocol":0}`, Request{Type: "hello", Protocol: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFrame([]byte(tc.frame))
			if err != nil {
				t.Fatalf("parseFrame(%s) error: %v", tc.frame, err)
			}
			if got != tc.want {
				t.Fatalf("parseFrame(%s) = %+v, want %+v", tc.frame, got, tc.want)
			}
		})
	}
}

func TestParseFrameInvalid(t *testing.T) {
	cases := []string{
		``,
		`   `,
		`null`,
		`[1,2]`,
		`"hello"`,
		`{}`,
		`{"protocol":1}`,                        // missing type
		`{"type":"hello"}`,                      // hello without protocol
		`{"type":"hello","protocol":1,"id":1}`,  // id unknown for hello
		`{"type":"health","id":"1"}`,            // string id
		`{"type":"health","id":1.5}`,            // non-integer id
		`{"type":"health","id":1e2}`,            // exponent id
		`{"type":"health","id":1,"protocol":1}`, // protocol unknown for health
		`{"type":"system.status","id":null}`,    // null id
		`{"type":"system.status"}`,              // missing id
		`{"type":null}`,                         // null type
		`{"type":1}`,                            // non-string type
		`{"type":"hello","protocol":"1"}`,       // string protocol
		`{"type":"hello","protocol":1.0}`,       // non-integer protocol
		`{"type":"hello","protocol":true}`,      // bool protocol
		`{"type":"wat","id":1}`,                 // unknown type with extra field
		`{"type":"hello","protocol":1,"x":1}`,   // unknown field
		`{"type":"hello","protocol":1,"type":"hello"}`,           // duplicate key
		`{"type":"hello","protocol":1} {"type":"health","id":1}`, // trailing JSON
		`{"type":"hello","protocol":1} garbage`,
		`{"type":"hello","protocol":1,}`, // trailing comma (invalid JSON)
		`{"type":"hello","protocol":`,
	}
	for _, frame := range cases {
		t.Run(frame, func(t *testing.T) {
			if _, err := parseFrame([]byte(frame)); err == nil {
				t.Fatalf("parseFrame(%s) unexpectedly succeeded", frame)
			} else if failCode(err) != CodeInvalidMessage {
				t.Fatalf("parseFrame(%s) code = %s, want invalid_message", frame, failCode(err))
			}
		})
	}
}

func TestParseFrameInvalidUTF8(t *testing.T) {
	frame := []byte{'{', '"', 't', 'y', 'p', 'e', '"', ':', '"', 'h', 'e', 'l', 'l', 'o', '"', 0xff, '}'}
	if _, err := parseFrame(frame); err == nil {
		t.Fatal("parseFrame with invalid UTF-8 unexpectedly succeeded")
	}
}

func TestEncodeResponsesExact(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"hello", string(EncodeHello()), `{"type":"hello","protocol":1,"service":"omatorrent-service"}`},
		{"health ok", string(EncodeHealth(3, true)), `{"type":"health","protocol":1,"id":3,"service":"ready","backend":"ok"}`},
		{"health unavailable", string(EncodeHealth(3, false)), `{"type":"health","protocol":1,"id":3,"service":"ready","backend":"unavailable"}`},
		{
			"status ok",
			string(EncodeStatus(9, true, StatusData{AppVersion: "v5.2.3", WebAPIVersion: "2.15.1", DlSpeed: 10, UpSpeed: 20, TorrentsTotal: 3})),
			`{"type":"system.status","protocol":1,"id":9,"qbittorrent":"ok","app_version":"v5.2.3","webapi_version":"2.15.1","dl_speed":10,"up_speed":20,"torrents_total":3}`,
		},
		{"status degraded", string(EncodeStatus(9, false, StatusData{})), `{"type":"system.status","protocol":1,"id":9,"qbittorrent":"unavailable"}`},
		{"error", string(EncodeError(CodeVersionMismatch)), `{"type":"error","protocol":1,"code":"version_mismatch"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got  %s\nwant %s", tc.got, tc.want)
			}
		})
	}
}
