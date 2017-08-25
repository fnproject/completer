package graph

import (
	"testing"

	"github.com/fnproject/completer/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type MockedListener struct {
	mock.Mock
}

func (mock *MockedListener) OnCompleteStage(stage *CompletionStage, result *model.CompletionResult) {
	mock.Called(stage, result)
}

func (mock *MockedListener) OnCompleteGraph() {
	mock.Called()
}

func (mock *MockedListener) OnExecuteStage(stage *CompletionStage, result []*model.CompletionResult) {
	mock.Called(stage, result)
}

func (mock *MockedListener) OnComposeStage(stage *CompletionStage, composedStage *CompletionStage) {
	mock.Called(stage, composedStage)
}

func TestShouldCreateGraph(t *testing.T) {

	graph := New("graph", "function", nil)
	assert.Equal(t, "graph", graph.ID)
	assert.Equal(t, "function", graph.FunctionID)

}

func TestShouldCreateStageIds(t *testing.T) {

	graph := New("graph", "function", nil)
	assert.Equal(t, "0", graph.NextStageID())
	withSimpleStage(graph, false)
	assert.Equal(t, "1", graph.NextStageID())

}

func TestShouldTriggerNewValueOnAdd(t *testing.T) {
	m := &MockedListener{}

	m.On("OnExecuteStage", mock.AnythingOfType("*graph.CompletionStage"), []*model.CompletionResult{}).Return()

	g := New("graph", "function", m)

	s := withSimpleStage(g, true)

	m.AssertExpectations(t)
	assertStagePending(t, s)
}

func TestShouldNotTriggerNewValueOnNonTrigger(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	s := withSimpleStage(g, false)

	m.AssertExpectations(t)
	assertStagePending(t, s)

}

func TestShouldCompleteValue(t *testing.T) {
	m := &MockedListener{}
	g := New("graph", "function", m)

	m.On("OnExecuteStage", mock.AnythingOfType("*graph.CompletionStage"), []*model.CompletionResult{}).Return()
	s := withSimpleStage(g, true)
	value := model.NewBlobDatum(sampleBlob("blob"))
	result := model.NewSuccessfulResult(value)

	g.UpdateWithEvent(&model.StageCompletedEvent{StageId: string(s.ID), Result: result}, true)

	m.AssertExpectations(t)
	assertStageCompletedSuccessfullyWith(t, s, result)
}

func TestShouldTriggerOnCompleteSuccess(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	s1 := withSimpleStage(g, false)
	s2 := withAppendedStage(g, s1, false)

	// No triggers
	m.AssertExpectations(t)

	value := model.NewBlobDatum(sampleBlob("blob"))
	result := model.NewSuccessfulResult(value)

	m.On("OnExecuteStage", s2, []*model.CompletionResult{result}).Return()
	g.UpdateWithEvent(&model.StageCompletedEvent{StageId: string(s1.ID), Result: result}, true)
	m.AssertExpectations(t)

}

func TestShouldTriggerOnWhenDependentsAreComplete(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	s1 := withSimpleStage(g, false)
	value := model.NewBlobDatum(sampleBlob("blob"))

	result := model.NewSuccessfulResult(value)
	s1.complete(result)

	m.On("OnExecuteStage", mock.AnythingOfType("*graph.CompletionStage"), []*model.CompletionResult{result}).Return()
	withAppendedStage(g, s1, true)

	// No triggers
	m.AssertExpectations(t)

}

func TestShouldPropagateFailureToSecondStage(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	s1 := withSimpleStage(g, false)
	s2 := withAppendedStage(g, s1, false)

	// No triggers
	m.AssertExpectations(t)

	value := model.NewBlobDatum(sampleBlob("blob"))
	result := model.NewFailedResult(value)

	m.On("OnCompleteStage", s2, result).Return()
	g.UpdateWithEvent(&model.StageCompletedEvent{StageId: string(s1.ID), Result: result}, true)
	m.AssertExpectations(t)

}

func TestShouldNotTriggerOnSubsequentCompletion(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	s1 := withSimpleStage(g, false)
	withAppendedStage(g, s1, false)

	// No triggers
	m.AssertExpectations(t)

	value := model.NewBlobDatum(sampleBlob("blob"))
	result := model.NewSuccessfulResult(value)

	g.UpdateWithEvent(&model.StageCompletedEvent{StageId: string(s1.ID), Result: result}, false)
	m.AssertExpectations(t)

}

//   r:= supply(()->1) // s1
// 	    .thenCompose(()->supply(()->2) // s3) //  s2
func TestShouldTriggerCompose(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)
	initial := model.NewBlobDatum(sampleBlob("blob"))
	result := model.NewSuccessfulResult(initial)
	s1 := withSimpleStage(g, false)
	s1.complete(result)
	m.On("OnExecuteStage", mock.AnythingOfType("*graph.CompletionStage"), []*model.CompletionResult{result}).Return()
	s2 := withComposeStage(g, s1, true)
	m.AssertExpectations(t)

	s3 := withSimpleStage(g, false)

	// complete S2 with a ref to s3
	m.On("OnComposeStage", s2, s3).Return()
	g.UpdateWithEvent(&model.StageComposedEvent{StageId: string(s2.ID), ComposedStageId: string(s3.ID)}, true)

	completed :=
		&model.FaasInvocationCompletedEvent{
			StageId: string(s2.ID),
			Result:  model.NewSuccessfulResult(model.NewStageRefDatum(string(s3.ID))),
		}
	g.UpdateWithEvent(completed, true)

	assertStagePending(t, s2)

	result2 := model.NewSuccessfulResult(model.NewBlobDatum(sampleBlob("New Blob")))
	// s2 should now  be completed with s2's result
	m.On("OnCompleteStage", s2, result2).Return()

	g.UpdateWithEvent(&model.StageCompletedEvent{StageId: string(s3.ID), Result: result2}, true)

	// No triggers
	m.AssertExpectations(t)

}

func TestShouldFailNodeWhenPartiallyRecovered(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)
	s := withSimpleStage(g, false)
	m.On("OnCompleteStage", s, stageRecoveryFailedResult).Return()

	g.Recover()
	m.AssertExpectations(t)

}

func TestShouldGetAllStages(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	assert.Empty(t, g.stages)
	s := withSimpleStage(g, false)
	assert.Equal(t, map[string]*CompletionStage{"0": s}, g.stages)
}

func TestShouldRejectUnknownOperationStage(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	cmd := &model.AddChainedStageRequest{
		GraphId:   "graph",
		Operation: model.CompletionOperation_unknown_operation,
		Closure:   &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Deps:      []string{},
	}

	assert.NotNil(t, g.ValidateCommand(cmd))
}

func TestShouldRejectDuplicateStage(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)
	s := withSimpleStage(g, false)

	event := &model.StageAddedEvent{
		StageId:      string(s.ID),
		Op:           model.CompletionOperation_supply,
		Closure:      &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Dependencies: []string{},
	}
	assert.Panics(t, func() { g.UpdateWithEvent(event, false) })
}

func TestShouldRejectStageWithInsufficientDependencies(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	cmd := &model.AddChainedStageRequest{
		GraphId:   "graph",
		Operation: model.CompletionOperation_thenApply,
		Closure:   &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Deps:      []string{},
	}
	assert.NotNil(t, g.ValidateCommand(cmd))
}

func TestShouldRejectStageWithTooManyDependencies(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)

	cmd := &model.AddChainedStageRequest{
		GraphId:   "graph",
		Operation: model.CompletionOperation_thenApply,
		Closure:   &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Deps:      []string{"s1", "s2"},
	}
	assert.NotNil(t, g.ValidateCommand(cmd))
}

func TestShouldRejectStageWithUnknownDependency(t *testing.T) {
	m := &MockedListener{}
	g := New("graph", "function", m)

	cmd := &model.AddChainedStageRequest{
		GraphId:   "graph",
		Operation: model.CompletionOperation_thenApply,
		Closure:   &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Deps:      []string{"unknown"},
	}
	assert.NotNil(t, g.ValidateCommand(cmd))
}
func TestShouldCompleteEmptyGraph(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)
	m.On("OnCompleteGraph").Return()
	g.UpdateWithEvent(&model.GraphCommittedEvent{GraphId: "graph"}, true)
	m.AssertExpectations(t)
}

func TestShouldNotCompletePendingGraph(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)
	withSimpleStage(g, false)
	assert.False(t, g.IsCompleted())
	g.UpdateWithEvent(&model.GraphCommittedEvent{GraphId: "graph"}, true)
	m.AssertExpectations(t)
}

func TestShouldPreventAddingStageToCompletedGraph(t *testing.T) {
	m := &MockedListener{}

	g := New("graph", "function", m)
	m.On("OnCompleteGraph").Return()
	g.UpdateWithEvent(&model.GraphCommittedEvent{GraphId: "graph"}, true)
	g.UpdateWithEvent(&model.GraphCompletedEvent{GraphId: "graph", FunctionId: "function"}, true)

	cmd := &model.AddChainedStageRequest{
		GraphId:   "graph",
		Operation: model.CompletionOperation_supply,
		Closure:   &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Deps:      []string{},
	}
	assert.NotNil(t, g.ValidateCommand(cmd))
	m.AssertExpectations(t)
}

func withSimpleStage(g *CompletionGraph, trigger bool) *CompletionStage {
	event := &model.StageAddedEvent{
		StageId:      g.NextStageID(),
		Op:           model.CompletionOperation_supply,
		Closure:      &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Dependencies: []string{},
	}

	g.UpdateWithEvent(event, trigger)
	return g.GetStage(event.StageId)
}

func withAppendedStage(g *CompletionGraph, s *CompletionStage, trigger bool) *CompletionStage {
	event := &model.StageAddedEvent{
		StageId:      g.NextStageID(),
		Op:           model.CompletionOperation_thenApply,
		Closure:      &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Dependencies: []string{string(s.ID)},
	}

	g.UpdateWithEvent(event, trigger)
	return g.GetStage(event.StageId)
}

func withComposeStage(g *CompletionGraph, s *CompletionStage, trigger bool) *CompletionStage {
	event := &model.StageAddedEvent{
		StageId:      g.NextStageID(),
		Op:           model.CompletionOperation_thenCompose,
		Closure:      &model.BlobDatum{BlobId: "1", ContentType: "application/octet-stream"},
		Dependencies: []string{string(s.ID)},
	}

	g.UpdateWithEvent(event, trigger)
	return g.GetStage(event.StageId)
}

func assertStagePending(t *testing.T, s *CompletionStage) {
	assert.False(t, s.IsResolved())
	assert.False(t, s.IsFailed())
	assert.False(t, s.IsSuccessful())
}
func assertStageCompletedSuccessfullyWith(t *testing.T, s *CompletionStage, result *model.CompletionResult) {
	assert.Equal(t, result, s.result)
	assert.True(t, s.IsSuccessful())
	assert.True(t, s.IsResolved())
}

func sampleBlob(id string) *model.BlobDatum {
	return &model.BlobDatum{
		BlobId:      id,
		ContentType: "content/type",
		Length:      101,
	}
}
