package protocol

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/fnproject/flow/model"
	"github.com/fnproject/flow/persistence"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

// DatumFromPart reads a model Datum Object from a multipart part
func DatumFromPart(store persistence.BlobStore, part *multipart.Part) (*model.Datum, error) {
	return readDatum(store, part, part.Header)
}

// DatumFromRequest reads a model Datum Object from an HTTP request
func DatumFromRequest(store persistence.BlobStore, req *http.Request) (*model.Datum, error) {
	return readDatum(store, req.Body, textproto.MIMEHeader(req.Header))
}

// CompletionResultFromRequest reads a Datum and completion result from an incoming request
func CompletionResultFromRequest(store persistence.BlobStore, req *http.Request) (*model.CompletionResult, error) {
	datum, err := readDatum(store, req.Body, textproto.MIMEHeader(req.Header))
	if err != nil {
		return nil, err
	}

	resultStatusHeader := req.Header.Get(HeaderResultStatus)
	var success bool
	if resultStatusHeader == "" {
		return nil, ErrMissingResultStatus
	}
	success, err = statusFromHeader(resultStatusHeader)
	if err != nil {
		return nil, err
	}

	return &model.CompletionResult{
		Successful: success,
		Datum:      datum,
	}, nil
}

func statusFromHeader(statusString string) (bool, error) {
	switch statusString {
	case ResultStatusSuccess:
		return true, nil
	case ResultStatusFailure:
		return false, nil
	default:
		return false, ErrInvalidResultStatus
	}
}

// CompletionResultFromEncapsulatedResponse returns a result expressed as HTTP in HTTP (body of outer req is A whole HTTP result frame) this is here to overcome the lack of outbound headers for default functions
func CompletionResultFromEncapsulatedResponse(store persistence.BlobStore, r *http.Response) (*model.CompletionResult, error) {

	actualResponse, err := http.ReadResponse(bufio.NewReader(r.Body), nil)
	if err != nil {
		return nil, fmt.Errorf("invalid encapsulated HTTP frame: %s", err.Error())
	}
	datum, err := readDatum(store, actualResponse.Body, textproto.MIMEHeader(actualResponse.Header))
	if err != nil {
		return nil, err
	}
	statusString := actualResponse.Header.Get(HeaderResultStatus)

	resultStatus, err := statusFromHeader(statusString)
	if err != nil {
		return nil, err
	}
	return &model.CompletionResult{Successful: resultStatus, Datum: datum}, nil
}

func readDatum(store persistence.BlobStore, part io.Reader, header textproto.MIMEHeader) (*model.Datum, error) {

	datumType := header.Get(HeaderDatumType)
	if datumType == "" {
		return nil, ErrMissingDatumType
	}

	switch datumType {
	case DatumTypeBlob:

		blob, err := readBlob(store, part, header, false)
		if err != nil {
			return nil, err
		}
		return &model.Datum{
			Val: &model.Datum_Blob{Blob: blob},
		}, nil

	case DatumTypeEmpty:
		return &model.Datum{Val: &model.Datum_Empty{Empty: &model.EmptyDatum{}}}, nil

	case DatumTypeError:
		errorContentType := header.Get(HeaderContentType)
		if errorContentType == "" {
			return nil, ErrMissingContentType
		}
		if errorContentType != "text/plain" {
			return nil, ErrInvalidContentType
		}

		errorTypeString := header.Get(HeaderErrorType)
		if "" == errorTypeString {
			return nil, ErrMissingErrorType
		}

		pbErrorTypeString := strings.Replace(errorTypeString, "-", "_", -1)

		// Unrecognised errors are coerced to unknown
		var pbErrorType model.ErrorDatumType
		if val, got := model.ErrorDatumType_value[pbErrorTypeString]; got {
			pbErrorType = model.ErrorDatumType(val)
		} else {
			pbErrorType = model.ErrorDatumType_unknown_error
		}

		buf := new(bytes.Buffer)
		_, err := buf.ReadFrom(part)
		if err != nil {
			return nil, fmt.Errorf("failed to read body")
		}

		return &model.Datum{
			Val: &model.Datum_Error{
				Error: &model.ErrorDatum{Type: pbErrorType, Message: buf.String()},
			},
		}, nil

	case DatumTypeStageRef:
		stageID := header.Get(HeaderStageRef)
		if stageID == "" {
			return nil, ErrMissingStageRef
		}
		return &model.Datum{Val: &model.Datum_StageRef{StageRef: &model.StageRefDatum{StageRef: string(stageID)}}}, nil

	case DatumTypeHTTPReq:
		methodString := header.Get(HeaderMethod)
		if "" == methodString {
			return nil, ErrMissingHTTPMethod
		}
		method, methodRecognized := model.HTTPMethod_value[strings.ToLower(methodString)]
		if !methodRecognized {
			return nil, ErrInvalidHTTPMethod
		}
		var headers []*model.HTTPHeader
		for hk, hvs := range header {
			if strings.HasPrefix(strings.ToLower(hk), strings.ToLower(HeaderHeaderPrefix)) {
				for _, hv := range hvs {
					headers = append(headers, &model.HTTPHeader{Key: hk[len(HeaderHeaderPrefix):], Value: hv})
				}
			}
		}

		blob, err := readBlob(store, part, header, true)
		if err != nil {
			return nil, err
		}
		return &model.Datum{Val: &model.Datum_HttpReq{HttpReq: &model.HTTPReqDatum{Body: blob, Headers: headers, Method: model.HTTPMethod(method)}}}, nil

	case DatumTypeHTTPResp:
		resultCodeString := header.Get(HeaderResultCode)
		if "" == resultCodeString {
			return nil, ErrMissingResultCode
		}
		resultCode, err := strconv.ParseUint(resultCodeString, 10, 32)
		if err != nil {
			return nil, ErrInvalidResultCode
		}
		var headers []*model.HTTPHeader
		for hk, hvs := range header {
			if strings.HasPrefix(strings.ToLower(hk), strings.ToLower(HeaderHeaderPrefix)) {
				for _, hv := range hvs {
					headers = append(headers, &model.HTTPHeader{Key: hk[len(HeaderHeaderPrefix):], Value: hv})
				}
			}
		}
		blob, err := readBlob(store, part, header, true)
		if err != nil {
			return nil, err
		}
		return &model.Datum{Val: &model.Datum_HttpResp{HttpResp: &model.HTTPRespDatum{Body: blob, Headers: headers, StatusCode: uint32(resultCode)}}}, nil
	default:
		return nil, ErrInvalidDatumType
	}
}

func readBlob(store persistence.BlobStore, part io.Reader, header textproto.MIMEHeader, allowNil bool) (*model.BlobDatum, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(part)

	if err != nil {
		return nil, fmt.Errorf("failed to read blob datum from body")
	}

	data := buf.Bytes()
	if allowNil && len(data) == 0 {
		return nil, nil
	}
	contentType := header.Get(HeaderContentType)
	if "" == contentType {
		return nil, ErrMissingContentType
	}

	blob, err := store.CreateBlob(contentType, data)
	if err != nil {
		return nil, err
	}
	return blob, nil
}
