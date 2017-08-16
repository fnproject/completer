package actor

import (
	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/fnproject/completer/model"
	"net/http"
	"github.com/sirupsen/logrus"
	"time"
	"bytes"
	"mime/multipart"
	"fmt"
	"strings"
	"io"
	"net/textproto"
)

type graphExecutor struct {
	faasAddr string
	client   httpClient
	log      *logrus.Entry
}

// For mocking
type httpClient interface {
	Do(*http.Request) (*http.Response, error)
}

// ExecHandler abstracts the FaaS execution backend
// implementations must handle all errors and return an appropriate invocation responser
type ExecHandler interface {
	HandleInvokeStageRequest(msg *model.InvokeStageRequest) *model.FaasInvocationResponse
	HandleInvokeFunctionRequest(msg *model.InvokeFunctionRequest) *model.FaasInvocationResponse
}

func NewExecutor(faasAddress string) actor.Actor {
	client := &http.Client{}
	// TODO configure timeouts
	client.Timeout = 300 * time.Second

	return &graphExecutor{faasAddr: faasAddress,
		log: logrus.WithField("logger", "executor_actor").WithField("faas_url", faasAddress),
		client: client,
	}
}

func (exec *graphExecutor) Receive(context actor.Context) {
	sender := context.Sender();
	switch msg := context.Message().(type) {
	case *model.InvokeStageRequest:
		go func() {
			sender.Tell(exec.HandleInvokeStageRequest(msg))
		}()
	case *model.InvokeFunctionRequest:
		go func() {
			sender.Tell(exec.HandleInvokeFunctionRequest(msg))
		}()
	}
}

func (exec *graphExecutor) HandleInvokeStageRequest(msg *model.InvokeStageRequest) *model.FaasInvocationResponse {
	stageLog := exec.log.WithFields(logrus.Fields{"graph_id": msg.GraphId, "stage_id": msg.StageId, "function_id": msg.FunctionId, "operation": msg.Operation})
	stageLog.Info("Running Stage")

	buf := new(bytes.Buffer)

	partWriter := multipart.NewWriter(buf)

	for _, datum := range msg.Args {
		err := model.WritePartFromDatum(datum, partWriter)
		if err != nil {
			log.Error("Failed to create multipart body", err)
			return stageFailed(msg, model.ErrorDatumType_stage_failed, "Error creating stage invoke request")

		}
	}
	partWriter.Close()

	req, _ := http.NewRequest("POST", exec.faasAddr+msg.FunctionId, buf)
	req.Header.Set("Content-type", fmt.Sprintf("multipart/form-data; boundary=\"%s\"", partWriter.Boundary()))
	req.Header.Set("FnProject-ThreadID", msg.GraphId)
	req.Header.Set("FnProject-StageID", msg.StageId)
	resp, err := exec.client.Do(req)
	if err != nil {
		return stageFailed(msg, model.ErrorDatumType_stage_failed, "Http error invoking stage")
	}

	if resp.StatusCode != 200 {
		stageLog.WithField("http_status", fmt.Sprintf("%d", resp.StatusCode)).Error("Got non-200 error from FaaS endpoint")

		if resp.StatusCode == 504 {
			return &model.FaasInvocationResponse{GraphId: msg.GraphId, StageId: msg.StageId, FunctionId: msg.FunctionId, Result: model.NewInternalErrorResult(model.ErrorDatumType_stage_timeout, "stage timed out")}
		}
		return stageFailed(msg, model.ErrorDatumType_stage_failed, fmt.Sprintf("Invalid http response from functions platform code %d", resp.StatusCode))
	}
	result, err := model.CompletionResultFromResponse(resp)
	if err != nil {
		stageLog.Error("Failed to read result from functions service", err)
		return stageFailed(msg, model.ErrorDatumType_invalid_stage_response, "Failed to read result from functions service")

	}
	stageLog.WithField("successful", fmt.Sprintf("%s", result.Successful)).Info("Got stage response")
	return &model.FaasInvocationResponse{GraphId: msg.GraphId, StageId: msg.StageId, FunctionId: msg.FunctionId, Result: result}
}

func stageFailed(msg *model.InvokeStageRequest, errorType model.ErrorDatumType, errorMessage string) *model.FaasInvocationResponse {
	return &model.FaasInvocationResponse{GraphId: msg.GraphId, StageId: msg.StageId, FunctionId: msg.FunctionId, Result: model.NewInternalErrorResult(errorType, errorMessage)}
}

func (exec *graphExecutor) HandleInvokeFunctionRequest(msg *model.InvokeFunctionRequest) *model.FaasInvocationResponse {
	datum := msg.Arg

	method := strings.ToUpper(model.HttpMethod_name[int32(datum.Method)])
	stageLog := exec.log.WithFields(logrus.Fields{"graph_id": msg.GraphId, "stage_id": msg.StageId, "target_function_id": msg.FunctionId, "method": method})
	stageLog.Info("Sending function invocation")

	var bodyReader io.Reader

	if datum.Body != nil {
		bodyReader = bytes.NewReader(datum.Body.DataString)
	} else {
		bodyReader = http.NoBody
	}

	req, err := http.NewRequest(strings.ToUpper(method), exec.faasAddr+msg.FunctionId, bodyReader)
	if err != nil {
		log.Error("Failed to create http request:", err)
		return invokeFailed(msg, "Failed to create HTTP request")
	}

	if datum.Body != nil {
		req.Header.Set("Content-Type", datum.Body.ContentType)
	}

	resp, err := exec.client.Do(req)

	if err != nil {
		log.Error("Http error calling functions service:", err)
		return invokeFailed(msg, "Failed to call function")

	}

	buf := &bytes.Buffer{}
	_, err = buf.ReadFrom(resp.Body)
	if err != nil {
		log.Error("Error reading data from function:", err)
		return invokeFailed(msg, "Failed to call function could not read response")

	}

	var contentType = resp.Header.Get("Content-type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	var headers []*model.HttpHeader = make([]*model.HttpHeader, 0)
	for headerName, valList := range resp.Header {
		// Don't copy content type into headers
		if textproto.CanonicalMIMEHeaderKey(headerName) == "Content-Type" {
			continue
		}
		for _, val := range valList {
			headers = append(headers, &model.HttpHeader{Key: headerName, Value: val})
		}
	}

	resultDatum := &model.Datum{
		Val: &model.Datum_HttpResp{
			HttpResp: &model.HttpRespDatum{
				Headers: headers,
				Body: &model.BlobDatum{
					ContentType: contentType,
					DataString:  buf.Bytes()},
				StatusCode: uint32(resp.StatusCode)}}}

	var boolResult bool
	// assume any non-error codes are success
	// TODO doc in spec
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		boolResult = true
	} else {
		boolResult = false
	}

	result := &model.CompletionResult{Successful: boolResult, Datum: resultDatum}
	return &model.FaasInvocationResponse{GraphId: msg.GraphId, StageId: msg.StageId, FunctionId: msg.FunctionId, Result: result}
}

func invokeFailed(msg *model.InvokeFunctionRequest, errorMessage string) *model.FaasInvocationResponse {
	return &model.FaasInvocationResponse{GraphId: msg.GraphId, StageId: msg.StageId, FunctionId: msg.FunctionId, Result: model.NewInternalErrorResult(model.ErrorDatumType_function_invoke_failed, errorMessage)}
}
