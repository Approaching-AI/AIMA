package openclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	mooerStatusOK = 1
	mooerTimeout  = 60 * time.Second
)

type asrUpload struct {
	Model          string
	ResponseFormat string
	Filename       string
	AudioData      []byte
}

type mooerRecognizeRequest struct {
	ReqID     string
	AudioData []byte
}

type mooerRecognizeResponse struct {
	Status int32
	Text   string
	Tokens []int32
}

type mooerCodec struct{}

var _ encoding.Codec = mooerCodec{}

var mooerRecognize = invokeMooerRecognize

func (mooerCodec) Name() string { return "proto" }

func (mooerCodec) Marshal(v any) ([]byte, error) {
	req, ok := v.(*mooerRecognizeRequest)
	if !ok {
		return nil, fmt.Errorf("mooer codec: unexpected request type %T", v)
	}

	var cfg []byte
	if req.ReqID != "" {
		cfg = protowire.AppendTag(cfg, 1, protowire.BytesType)
		cfg = protowire.AppendString(cfg, req.ReqID)
	}

	var out []byte
	if len(cfg) > 0 {
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, cfg)
	}
	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendBytes(out, req.AudioData)
	return out, nil
}

func (mooerCodec) Unmarshal(data []byte, v any) error {
	resp, ok := v.(*mooerRecognizeResponse)
	if !ok {
		return fmt.Errorf("mooer codec: unexpected response type %T", v)
	}

	var err error
	*resp = mooerRecognizeResponse{}
	for len(data) > 0 {
		var (
			num protowire.Number
			typ protowire.Type
		)
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return fmt.Errorf("mooer codec: invalid response tag")
		}
		data = data[n:]
		switch num {
		case 1:
			var nested []byte
			nested, data, err = consumeBytesField(typ, data)
			if err != nil {
				return err
			}
			resp.Status, err = consumeMooerHeader(nested)
			if err != nil {
				return err
			}
		case 2:
			var nested []byte
			nested, data, err = consumeBytesField(typ, data)
			if err != nil {
				return err
			}
			resp.Text, resp.Tokens, err = consumeMooerPayload(nested)
			if err != nil {
				return err
			}
		default:
			data, err = skipUnknownField(typ, data)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func parseASRUpload(r *http.Request, body []byte) (*asrUpload, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return nil, nil
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, fmt.Errorf(`{"error":"multipart request missing boundary"}`)
	}

	upload := &asrUpload{}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf(`{"error":"invalid multipart form body"}`)
		}

		name := part.FormName()
		data, readErr := io.ReadAll(io.LimitReader(part, 100<<20))
		part.Close()
		if readErr != nil {
			return nil, fmt.Errorf(`{"error":"failed to read multipart field %q"}`, name)
		}

		switch name {
		case "model":
			upload.Model = strings.TrimSpace(string(data))
		case "response_format":
			upload.ResponseFormat = strings.TrimSpace(string(data))
		case "file":
			upload.Filename = part.FileName()
			upload.AudioData = append([]byte(nil), data...)
		}
	}
	return upload, nil
}

func isMooERBackend(backend *Backend) bool {
	if backend == nil {
		return false
	}
	return strings.Contains(strings.ToLower(backend.EngineType), "mooer")
}

func (d *Deps) handleMooERASR(w http.ResponseWriter, r *http.Request, backend *Backend, upload *asrUpload) {
	if upload == nil || len(upload.AudioData) == 0 {
		http.Error(w, `{"error":"missing file field"}`, http.StatusBadRequest)
		return
	}

	resp, err := mooerRecognize(r.Context(), backend.Address, upload.AudioData)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"mooer recognize failed: %s"}`, err), http.StatusBadGateway)
		return
	}
	if resp.Status != mooerStatusOK {
		http.Error(w, `{"error":"mooer recognize returned non-OK status"}`, http.StatusBadGateway)
		return
	}

	responseFormat := strings.ToLower(strings.TrimSpace(upload.ResponseFormat))
	switch responseFormat {
	case "text", "srt", "vtt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, resp.Text)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": resp.Text})
	}
}

func invokeMooerRecognize(ctx context.Context, target string, audioData []byte) (*mooerRecognizeResponse, error) {
	target = strings.TrimPrefix(strings.TrimPrefix(target, "http://"), "https://")

	callCtx, cancel := context.WithTimeout(ctx, mooerTimeout)
	defer cancel()

	conn, err := grpc.DialContext(
		callCtx,
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.ForceCodec(mooerCodec{}),
			grpc.MaxCallRecvMsgSize(4<<20),
			grpc.MaxCallSendMsgSize(128<<20),
		),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial mooer %s: %w", target, err)
	}
	defer conn.Close()

	req := &mooerRecognizeRequest{
		ReqID:     fmt.Sprintf("aima-%d", time.Now().UnixNano()),
		AudioData: audioData,
	}
	resp := &mooerRecognizeResponse{}
	if err := conn.Invoke(callCtx, "/mooer.v1.ASR/Recognize", req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func consumeMooerHeader(data []byte) (int32, error) {
	var status int32
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return 0, fmt.Errorf("mooer codec: invalid header tag")
		}
		data = data[n:]
		if num == 1 && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return 0, fmt.Errorf("mooer codec: invalid header status")
			}
			status = int32(v)
			data = data[n:]
			continue
		}
		var err error
		data, err = skipUnknownField(typ, data)
		if err != nil {
			return 0, err
		}
	}
	return status, nil
}

func consumeMooerPayload(data []byte) (string, []int32, error) {
	var (
		text   string
		tokens []int32
	)
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", nil, fmt.Errorf("mooer codec: invalid payload tag")
		}
		data = data[n:]
		switch num {
		case 1:
			if typ != protowire.BytesType {
				return "", nil, fmt.Errorf("mooer codec: invalid payload text type")
			}
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return "", nil, fmt.Errorf("mooer codec: invalid payload text")
			}
			text = string(v)
			data = data[n:]
		case 2:
			switch typ {
			case protowire.VarintType:
				v, n := protowire.ConsumeVarint(data)
				if n < 0 {
					return "", nil, fmt.Errorf("mooer codec: invalid token value")
				}
				tokens = append(tokens, int32(v))
				data = data[n:]
			case protowire.BytesType:
				packed, n := protowire.ConsumeBytes(data)
				if n < 0 {
					return "", nil, fmt.Errorf("mooer codec: invalid packed token value")
				}
				for len(packed) > 0 {
					v, vn := protowire.ConsumeVarint(packed)
					if vn < 0 {
						return "", nil, fmt.Errorf("mooer codec: invalid packed token entry")
					}
					tokens = append(tokens, int32(v))
					packed = packed[vn:]
				}
				data = data[n:]
			default:
				return "", nil, fmt.Errorf("mooer codec: invalid token type")
			}
		default:
			var err error
			data, err = skipUnknownField(typ, data)
			if err != nil {
				return "", nil, err
			}
		}
	}
	return text, tokens, nil
}

func consumeBytesField(typ protowire.Type, data []byte) ([]byte, []byte, error) {
	if typ != protowire.BytesType {
		return nil, nil, fmt.Errorf("mooer codec: unexpected field type %d", typ)
	}
	v, n := protowire.ConsumeBytes(data)
	if n < 0 {
		return nil, nil, fmt.Errorf("mooer codec: invalid bytes field")
	}
	return v, data[n:], nil
}

func skipUnknownField(typ protowire.Type, data []byte) ([]byte, error) {
	n := protowire.ConsumeFieldValue(0, typ, data)
	if n < 0 {
		return nil, fmt.Errorf("mooer codec: invalid field value")
	}
	return data[n:], nil
}
