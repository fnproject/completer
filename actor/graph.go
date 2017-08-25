package actor

import (
	"fmt"
	"reflect"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	protoPersistence "github.com/AsynkronIT/protoactor-go/persistence"
	"github.com/fnproject/completer/graph"
	"github.com/fnproject/completer/model"
	"github.com/fnproject/completer/persistence"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/sirupsen/logrus"
)

// TODO: read this from configuration!
const (
	inactiveTimeout = time.Duration(24 * time.Hour)
	readTimeout     = time.Duration(5 * time.Minute)
)

type graphActor struct {
	PIDHolder
	graph    *graph.CompletionGraph
	log      *logrus.Entry
	executor *actor.PID
	persistence.Mixin
}

// NewGraphActor returns a pointer to a new graph actor
func NewGraphActor(executor *actor.PID) actor.Actor {
	return &graphActor{
		executor: executor,
		log:      logrus.New().WithField("logger", "graph_actor"),
	}
}

func (g *graphActor) Receive(context actor.Context) {
	g.log.Debugf("Processing message %s (recovering=%v)", reflect.TypeOf(context.Message()), g.Recovering())
	if g.Recovering() {
		if e, ok := context.Message().(model.Event); ok {
			g.receiveEvent(e)
		}
	} else {
		if c, ok := context.Message().(model.Command); ok {
			g.receiveCommand(c, context)
		} else {
			g.receiveMessage(context)
		}
	}
}

func (g *graphActor) initGraph(event *model.GraphCreatedEvent) {
	g.log = g.log.WithFields(logrus.Fields{"logger": "graph_actor", "graph_id": event.GraphId, "function_id": event.FunctionId})
	g.graph = graph.New(event.GraphId, event.FunctionId, g)
}

func (g *graphActor) receiveEvent(event model.Event) {

	switch e := event.(type) {

	case *model.GraphCreatedEvent:
		g.initGraph(e)

	default:
		if g.graph == nil {
			panic(fmt.Sprintf("Trying to replay event %v but graph is not initialized", reflect.TypeOf(event)))
		}
		g.graph.UpdateWithEvent(e, false)
	}
}

// if validation fails, this method will respond to the request with an appropriate error message
func (g *graphActor) validateCmd(cmd model.Command, context actor.Context) bool {
	switch msg := cmd.(type) {

	case *model.CreateGraphRequest:
		if g.graph != nil {
			g.log.Warn("Graph has already been created")
			context.Respond(model.NewGraphCreationError(msg.GetGraphId()))
			return false
		}

	default:
		if g.graph == nil {
			context.Respond(model.NewGraphNotFoundError(msg.GetGraphId()))
			return false
		}

		if validationError := g.graph.ValidateCommand(msg); validationError != nil {
			context.Respond(validationError)
			return false
		}
	}

	return true
}

func currentTimestamp() *timestamp.Timestamp {
	now := time.Now()

	return &timestamp.Timestamp{
		Seconds: now.Unix(),
		Nanos:   int32(now.Nanosecond()),
	}
}

func (g *graphActor) receiveCommand(cmd model.Command, context actor.Context) {
	if !g.validateCmd(cmd, context) {
		return
	}

	switch msg := cmd.(type) {

	case *model.CreateGraphRequest:
		g.log.Debug("Creating graph")
		event := &model.GraphCreatedEvent{GraphId: msg.GraphId, FunctionId: msg.FunctionId, Ts: currentTimestamp()}
		g.PersistReceive(event)
		g.initGraph(event)
		context.Respond(&model.CreateGraphResponse{GraphId: msg.GraphId})

	case *model.GetGraphStateRequest:
		g.log.Debug("Get graph state")
		context.Respond(g.createExternalState())

	case *model.AddChainedStageRequest:
		g.log.Debug("Adding chained stage")
		stageID := g.graph.NextStageID()

		g.persistAndUpdateGraph(&model.StageAddedEvent{
			StageId:      stageID,
			Op:           msg.Operation,
			Closure:      msg.Closure,
			Dependencies: msg.Deps,
			Ts:           currentTimestamp(),
		})

		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: stageID})

	case *model.AddCompletedValueStageRequest:
		g.log.Debug("Adding completed value stage")
		stageID := g.graph.NextStageID()

		g.persistAndUpdateGraph(&model.StageAddedEvent{
			StageId: stageID,
			Op:      msg.GetOperation(),
			Ts:      currentTimestamp(),
		})

		g.persistAndUpdateGraph(&model.StageCompletedEvent{
			StageId: stageID,
			Result:  msg.Result,
			Ts:      currentTimestamp(),
		})
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: stageID})

	case *model.AddDelayStageRequest:
		g.log.Debug("Adding delay stage")
		stageID := g.graph.NextStageID()

		g.persistAndUpdateGraph(&model.StageAddedEvent{
			StageId: stageID,
			Op:      msg.GetOperation(),
			Ts:      currentTimestamp(),
		})
		delayEvent := &model.DelayScheduledEvent{
			StageId: stageID,
			TimeMs:  timeMillis() + msg.DelayMs,
			Ts:      currentTimestamp(),
		}
		g.PersistReceive(delayEvent)
		g.scheduleDelay(delayEvent)
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: stageID})

	case *model.AddExternalCompletionStageRequest:
		g.log.Debug("Adding external completion stage")
		stageID := g.graph.NextStageID()

		g.persistAndUpdateGraph(&model.StageAddedEvent{
			StageId: stageID,
			Op:      msg.GetOperation(),
			Ts:      currentTimestamp(),
		})
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: stageID})

	case *model.AddInvokeFunctionStageRequest:
		g.log.Debug("Adding invoke stage")
		stageID := g.graph.NextStageID()

		g.persistAndUpdateGraph(&model.StageAddedEvent{
			StageId: stageID,
			Op:      msg.GetOperation(),
			Ts:      currentTimestamp(),
		})

		g.executor.Request(&model.InvokeFunctionRequest{
			GraphId:    g.graph.ID,
			StageId:    stageID,
			FunctionId: msg.FunctionId,
			Arg:        msg.Arg,
		}, g.GetSelf())
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: stageID})

	case *model.CompleteStageExternallyRequest:
		g.log.WithFields(logrus.Fields{"stage_id": msg.StageId}).Debug("Completing stage externally")
		stage := g.graph.GetStage(msg.StageId)
		completable := !stage.IsResolved()
		if completable {
			g.persistAndUpdateGraph(&model.StageCompletedEvent{
				StageId: msg.StageId,
				Result:  msg.Result,
				Ts:      currentTimestamp(),
			})
		}
		context.Respond(&model.CompleteStageExternallyResponse{GraphId: msg.GraphId, StageId: msg.StageId, Successful: completable})

	case *model.CommitGraphRequest:
		response := &model.CommitGraphProcessed{GraphId: msg.GraphId}
		if g.graph.IsCommitted() {
			// idempotent
			context.Respond(response)
			return
		}
		g.log.Debug("Committing graph")
		g.persistAndUpdateGraph(&model.GraphCommittedEvent{GraphId: msg.GraphId, Ts: currentTimestamp()})
		context.Respond(response)

	case *model.GetStageResultRequest:
		g.log.WithFields(logrus.Fields{"stage_id": msg.StageId}).Debug("Retrieving stage result")
		stage := g.graph.GetStage(msg.StageId)
		context.AwaitFuture(stage.WhenComplete(), func(result interface{}, err error) {
			if err != nil {
				context.Respond(model.NewStageCompletionError(msg.GraphId, msg.StageId))
				return
			}
			response := &model.GetStageResultResponse{
				GraphId: msg.GraphId,
				StageId: msg.StageId,
				Result:  result.(*model.CompletionResult),
			}
			context.Respond(response)
		})

	case *model.CompleteDelayStageRequest:
		if g.graph.IsCompleted() {
			// avoids accumulating duplicate StageCompletedEvents
			return
		}
		g.log.WithFields(logrus.Fields{"stage_id": msg.StageId}).Debug("Completing delayed stage")

		g.persistAndUpdateGraph(&model.StageCompletedEvent{
			StageId: msg.StageId,
			Result:  msg.Result,
			Ts:      currentTimestamp(),
		})

	case *model.FaasInvocationResponse:
		g.log.WithFields(logrus.Fields{"stage_id": msg.StageId}).Debug("Received fn invocation response")

		g.persistAndUpdateGraph(&model.FaasInvocationCompletedEvent{
			StageId: msg.StageId,
			Result:  msg.Result,
			Ts:      currentTimestamp(),
		})

	case *model.DeactivateGraphRequest:
		g.log.Debug("Telling supervisor graph is completed")
		// tell supervisor to remove us from active graphs
		context.Parent().Tell(msg)

	default:
		g.log.Debugf("Ignoring command of unknown type %v", reflect.TypeOf(msg))
	}
}

func (g *graphActor) receiveMessage(context actor.Context) {
	switch msg := context.Message().(type) {
	case *protoPersistence.RequestSnapshot:
		// snapshots are currently not supported
		// NOOP
		g.log.Debug("Ignoring snapshot request")

	case *actor.Started:
		g.log.Debugf("Started actor %s", g.GetSelf().Id)
		context.SetReceiveTimeout(inactiveTimeout)

	case *actor.ReceiveTimeout:
		if g.graph != nil {
			g.log.Debugf("Requesting passivation of inactive actor %s", g.GetSelf().Id)
			context.Parent().Tell(&model.DeactivateGraphRequest{GraphId: g.graph.ID})
		}

	case *protoPersistence.ReplayComplete:
		if g.graph != nil {
			g.log.Debug("Replay completed")
			g.graph.Recover()

			if g.graph.IsCompleted() {
				// graph is in read-only mode, so request passivation after read timeout
				context.SetReceiveTimeout(readTimeout)
			}
		}

	default:
		g.log.Debugf("Ignoring message of unknown type %v", reflect.TypeOf(msg))
	}
}

func (g *graphActor) scheduleDelay(event *model.DelayScheduledEvent) {
	// we always need to complete delay nodes from scratch to avoid completing twice
	delayMs := event.TimeMs - timeMillis()
	if delayMs > 0 {
		g.log.WithFields(logrus.Fields{"stage_id": event.StageId}).Debug("Scheduling delayed completion of stage")
		// Wait for the delay in a goroutine so we can complete the request in the meantime
		go func() {
			<-time.After(time.Duration(delayMs) * time.Millisecond)
			g.pid.Tell(&model.CompleteDelayStageRequest{
				GraphId: g.graph.ID,
				StageId: event.StageId,
				Result:  model.NewEmptyResult(),
			})
		}()
	} else {
		g.log.WithFields(logrus.Fields{"stage_id": event.StageId}).Debug("Queuing completion of delayed stage")
		g.pid.Tell(&model.CompleteDelayStageRequest{
			GraphId: g.graph.ID,
			StageId: event.StageId,
			Result:  model.NewEmptyResult(),
		})
	}
}

func timeMillis() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func (g *graphActor) createExternalState() *model.GetGraphStateResponse {
	stageOut := make(map[string]*model.GetGraphStateResponse_StageRepresentation)
	for _, s := range g.graph.GetStages() {
		var status string
		if s.IsFailed() {
			status = "failed"
		} else if s.IsSuccessful() {
			status = "successful"
		} else if s.IsTriggered() {
			status = "running"
		} else {
			status = "pending"
		}

		stageDeps := s.GetDeps()
		deps := make([]string, len(stageDeps))
		for i, dep := range stageDeps {
			deps[i] = dep.ID
		}

		rep := &model.GetGraphStateResponse_StageRepresentation{
			Type:         model.CompletionOperation_name[int32(s.GetOperation())],
			Status:       status,
			Dependencies: deps,
		}
		stageOut[s.ID] = rep
	}
	return &model.GetGraphStateResponse{
		GraphId:    g.graph.ID,
		FunctionId: g.graph.FunctionID,
		Stages:     stageOut,
	}
}

func (g *graphActor) OnExecuteStage(stage *graph.CompletionStage, results []*model.CompletionResult) {
	g.log.WithField("stage_id", stage.ID).Info("Executing Stage")
	msg := &model.InvokeStageRequest{
		FunctionId: g.graph.FunctionID,
		GraphId:    g.graph.ID,
		StageId:    stage.ID,
		Args:       results,
		Closure:    stage.GetClosure(),
		Operation:  stage.GetOperation(),
	}
	g.executor.Request(msg, g.GetSelf())
}

//OnCompleteStage indicates that a stage is finished and its result is available
func (g *graphActor) OnCompleteStage(stage *graph.CompletionStage, result *model.CompletionResult) {
	g.log.WithField("stage_id", stage.ID).Info("Completing stage in OnCompleteStage")
	g.persistAndUpdateGraph(&model.StageCompletedEvent{
		StageId: stage.ID,
		Result:  result,
		Ts:      currentTimestamp(),
	})
}

//OnCompose Stage indicates that another stage should be composed into this one
func (g *graphActor) OnComposeStage(stage *graph.CompletionStage, composedStage *graph.CompletionStage) {
	g.log.WithField("stage_id", stage.ID).Info("Composing stage in OnComposeStage")
	g.persistAndUpdateGraph(&model.StageComposedEvent{
		StageId:         stage.ID,
		ComposedStageId: composedStage.ID,
		Ts:              currentTimestamp(),
	})
}

//OnCompleteGraph indicates that the graph is now finished and cannot be modified
func (g *graphActor) OnCompleteGraph() {
	if g.Recovering() {
		return
	}
	g.persistAndUpdateGraph(&model.GraphCompletedEvent{
		GraphId:    g.graph.ID,
		FunctionId: g.graph.FunctionID,
		Ts:         currentTimestamp(),
	})
	g.GetSelf().Tell(&model.DeactivateGraphRequest{GraphId: g.graph.ID})
}

// persistAndUpdateGraph saves an event before applying it to the graph
func (g *graphActor) persistAndUpdateGraph(event model.Event) {
	if g.graph == nil {
		g.log.Errorf("Ignoring state update for event %v since graph is not initialized", reflect.TypeOf(event))
		return
	}
	g.PersistReceive(event)
	g.graph.UpdateWithEvent(event, !g.Recovering())
}
