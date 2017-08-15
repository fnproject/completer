package actor

import (
	"reflect"
	"time"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/AsynkronIT/protoactor-go/persistence"
	"github.com/fnproject/completer/graph"
	"github.com/fnproject/completer/model"
	proto "github.com/golang/protobuf/proto"
	"github.com/sirupsen/logrus"
	"github.com/fnproject/completer/graph"
	"time"
)

var (
	log = logrus.WithField("logger", "actor")
)

type graphSupervisor struct {
}

func (s *graphSupervisor) Receive(context actor.Context) {
	switch msg := context.Message().(type) {

	case *model.CreateGraphRequest:
		child, err := spawnGraphActor(msg.GraphId, context)
		if err != nil {
			log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Warn("Failed to spawn graph actor")
			return
		}
		child.Request(msg, context.Sender())

	default:
		if isGraphMessage(msg) {
			graphId := getGraphId(msg)
			child, found := findChild(context, graphId)
			if !found {
				log.WithFields(logrus.Fields{"graph_id": graphId}).Warn("No child actor found")
				context.Respond(NewGraphNotFoundError(graphId))
				return
			}
			child.Request(msg, context.Sender())
		}
	}
}

func isGraphMessage(msg interface{}) bool {
	return reflect.ValueOf(msg).Elem().FieldByName("GraphId").IsValid()
}

func getGraphId(msg interface{}) string {
	return reflect.ValueOf(msg).Elem().FieldByName("GraphId").String()
}

func findChild(context actor.Context, graphId string) (*actor.PID, bool) {
	fullId := context.Self().Id + "/" + graphId
	for _, pid := range context.Children() {
		if pid.Id == fullId {
			return pid, true
		}
	}
	return nil, false
}

// implements persistence.Provider
type Provider struct {
	providerState persistence.ProviderState
}

func (p *Provider) GetState() persistence.ProviderState {
	return p.providerState
}

func newInMemoryProvider(snapshotInterval int) persistence.Provider {
	return &Provider{
		providerState: persistence.NewInMemoryProvider(snapshotInterval),
	}
}

// TODO: Reified completion event listener
type completionEventListenerImpl struct {
	actor *graphActor
}
func (listener *completionEventListenerImpl) OnExecuteStage(stage *graph.CompletionStage, datum []*model.Datum) {}
func (listener *completionEventListenerImpl) OnCompleteStage(stage *graph.CompletionStage, result *model.CompletionResult) {}
func (listener *completionEventListenerImpl) OnComposeStage(stage *graph.CompletionStage, composedStage *graph.CompletionStage) {}
func (listener *completionEventListenerImpl) OnCompleteGraph() {}


// Graph actor

func spawnGraphActor(graphId string, context actor.Context) (*actor.PID, error) {

	provider := newInMemoryProvider(1)
	props := actor.FromInstance(&graphActor{graph: nil}).WithMiddleware(persistence.Using(provider))
	pid, err := context.SpawnNamed(props, graphId)
	return pid, err
}

type graphActor struct {
	graph *graph.CompletionGraph
	persistence.Mixin
	graph *graph.CompletionGraph
}

func (g *graphActor) persist(event proto.Message) error {
	g.PersistReceive(event)
	return nil
}

func (g *graphActor) applyGraphCreatedEvent(event *model.GraphCreatedEvent) {
	log.WithFields(logrus.Fields{"graph_id": event.GraphId, "function_id": event.FunctionId}).Debug("Creating completion graph")
	listener := &completionEventListenerImpl{actor: g}
	g.graph = graph.New(graph.GraphID(event.GraphId), event.FunctionId, listener)
}

func (g *graphActor) applyGraphCommittedEvent(event *model.GraphCommittedEvent) {
	log.WithFields(logrus.Fields{"graph_id": g.graph.ID, "function_id": g.graph.FunctionID}).Debug("Committing graph")
	g.graph.HandleCommitted()
}

func (g *graphActor) applyGraphCompletedEvent(event *model.GraphCompletedEvent, context actor.Context) {
	log.WithFields(logrus.Fields{"graph_id": g.graph.ID, "function_id": g.graph.FunctionID}).Debug("Completing graph")
	g.graph.HandleCompleted()
	// "poison pill"
	context.Self().Stop()
}

func (g *graphActor) applyStageAddedEvent(event *model.StageAddedEvent) {
	log.WithFields(logrus.Fields{"graph_id": g.graph.ID, "function_id": g.graph.FunctionID, "stage_id": event.StageId}).Debug("Adding stage")
	g.graph.HandleStageAdded(event, !g.Recovering())
}

func (g *graphActor) applyStageCompletedEvent(event *model.StageCompletedEvent) {
	log.WithFields(logrus.Fields{"graph_id": g.graph.ID, "function_id": g.graph.FunctionID, "stage_id": event.StageId}).Debug("Completing stage")
	g.graph.HandleStageCompleted(event, !g.Recovering())
}

func (g *graphActor) applyStageComposedEvent(event *model.StageComposedEvent) {
	log.WithFields(logrus.Fields{"graph_id": g.graph.ID, "function_id": g.graph.FunctionID, "stage_id": event.StageId}).Debug("Composing stage")
	g.graph.HandleStageComposed(event)
}

func (g *graphActor) applyDelayScheduledEvent(event *model.DelayScheduledEvent, context actor.Context) {
	// we always need to complete delay nodes from scratch to avoid completing twice
	delayMs := int64(event.DelayedTs) - time.Now().Unix() // TODO: is this right?
	if delayMs > 0 {
		log.WithFields(logrus.Fields{"graph_id": g.graph.ID, "function_id": g.graph.FunctionID, "stage_id": event.StageId}).Debug("Scheduled delayed completion of stage")
		// TODO: How do we actually delay this??
		context.Self().Tell(model.StageCompletedEvent{
			event.StageId,
			graph.SuccessfulResult(&model.Datum{Val: &model.Datum_Empty{Empty: &model.EmptyDatum{}}})})
	} else {
		log.WithFields(logrus.Fields{"graph_id": g.graph.ID, "function_id": g.graph.FunctionID, "stage_id": event.StageId}).Debug("Queuing completion of delayed stage")
		context.Self().Tell(model.StageCompletedEvent{
			event.StageId,
			graph.SuccessfulResult(&model.Datum{Val: &model.Datum_Empty{Empty: &model.EmptyDatum{}}})})
	}
}

func (g *graphActor) applyNoop(event interface{}) {

}

// process events
func (g *graphActor) receiveRecover(context actor.Context) {
}

func (g *graphActor) validateStages(stageIDs []uint32) bool {
	stages := make([]graph.StageID, len(stageIDs))
	for i, id := range stageIDs {
		stages[i] = graph.StageID(id)
	}
	return g.graph.GetStages(stages) != nil
}

// if validation fails, this method will respond to the request with an appropriate error message
func (g *graphActor) validateCmd(cmd interface{}, context actor.Context) bool {
	if isGraphMessage(cmd) {
		graphId := getGraphId(cmd)
		if g.graph == nil {
			context.Respond(NewGraphNotFoundError(graphId))
			return false
		} else if g.graph.IsCompleted() {
			context.Respond(NewGraphCompletedError(graphId))
			return false
		}
	}

	switch msg := cmd.(type) {

	case *model.AddDelayStageRequest:

	case *model.AddChainedStageRequest:
		if g.validateStages(msg.Deps) {
			context.Respond(NewGraphCompletedError(msg.GraphId))
			return false
		}
	}

	return true
}

// process commands
func (g *graphActor) receiveStandard(context actor.Context) {
	switch msg := context.Message().(type) {

	case *model.CreateGraphRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Creating graph")
		event := &model.GraphCreatedEvent{GraphId: msg.GraphId, FunctionId: msg.FunctionId}
		err := g.persist(event)
		if err != nil {
			context.Respond(NewGraphEventPersistenceError(msg.GraphId))
			return
		}
		g.applyGraphCreatedEvent(event)
		context.Respond(&model.CreateGraphResponse{GraphId: msg.GraphId})

	case *model.AddChainedStageRequest:
		if !g.validateCmd(msg, context) {
			return
		}
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding chained stage")
		event := &model.StageAddedEvent{
			StageId:      g.graph.NextStageID(),
			Op:           msg.Operation,
			Closure:      msg.Closure,
			Dependencies: msg.Deps,
		}
		err := g.persist(event)
		if err != nil {
			context.Respond(NewGraphEventPersistenceError(msg.GraphId))
			return
		}
		g.applyNoop(event)
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: event.StageId})

	case *model.AddCompletedValueStageRequest:
		if !g.validateCmd(msg, context) {
			return
		}
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding completed value stage")

		addedEvent := &model.StageAddedEvent{
			StageId: g.graph.NextStageID(),
			Op:      model.CompletionOperation_completedValue,
		}
		err := g.persist(addedEvent)
		if err != nil {
			context.Respond(NewGraphEventPersistenceError(msg.GraphId))
			return
		}
		g.applyNoop(addedEvent)

		completedEvent := &model.StageCompletedEvent{
			StageId: g.graph.NextStageID(),
			Result:  msg.Result,
		}
		err = g.persist(completedEvent)
		if err != nil {
			context.Respond(NewGraphEventPersistenceError(msg.GraphId))
			return
		}
		g.applyNoop(completedEvent)
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: addedEvent.StageId})

	case *model.AddDelayStageRequest:
		if !g.validateCmd(msg, context) {
			return
		}
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding delay stage")

		addedEvent := &model.StageAddedEvent{
			StageId: g.graph.NextStageID(),
			Op:      model.CompletionOperation_delay,
		}
		err := g.persist(addedEvent)
		if err != nil {
			context.Respond(NewGraphEventPersistenceError(msg.GraphId))
			return
		}
		g.applyNoop(addedEvent)

		delayEvent := &model.DelayScheduledEvent{
			StageId:   g.graph.NextStageID(),
			DelayedTs: uint64(time.Now().UnixNano()/1000000) + msg.DelayMs,
		}
		err = g.persist(delayEvent)
		if err != nil {
			context.Respond(NewGraphEventPersistenceError(msg.GraphId))
			return
		}
		g.applyNoop(delayEvent)

		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: addedEvent.StageId})

	case *model.AddExternalCompletionStageRequest:
		if !g.validateCmd(msg, context) {
			return
		}
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding external completion stage")
		event := &model.StageAddedEvent{
			StageId: g.graph.NextStageID(),
			Op:      model.CompletionOperation_externalCompletion,
		}
		err := g.persist(event)
		if err != nil {
			context.Respond(NewGraphEventPersistenceError(msg.GraphId))
			return
		}
		g.applyNoop(event)
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: event.StageId})

	case *model.AddInvokeFunctionStageRequest:
		if !g.validateCmd(msg, context) {
			return
		}
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding invoke stage")

		event := &model.StageAddedEvent{
			StageId: g.graph.NextStageID(),
			Op:      model.CompletionOperation_completedValue,
		}
		err := g.persist(event)
		if err != nil {
			context.Respond(NewGraphEventPersistenceError(msg.GraphId))
			return
		}
		g.applyNoop(event)

		/* TODO graph executor
		invokeReq := &model.InvokeFunctionRequest{
			GraphId:    msg.GraphId,
			StageId:    event.StageId,
			FunctionId: msg.FunctionId,
			Arg:        msg.Arg,
		}
		executor.Request(invokeReq)
		*/

		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: event.StageId})

	case *model.CompleteStageExternallyRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Completing stage externally")
		context.Respond(&model.CompleteStageExternallyResponse{GraphId: msg.GraphId, StageId: msg.StageId, Successful: true})

	case *model.CommitGraphRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Committing graph")
		context.Respond(&model.CommitGraphProcessed{GraphId: msg.GraphId})

	case *model.GetStageResultRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Retrieving stage result")
		datum := &model.Datum{
			Val: &model.Datum_Blob{
				Blob: &model.BlobDatum{ContentType: "text", DataString: []byte("foo")},
			},
		}
		result := &model.CompletionResult{Successful: true, Datum: datum}
		context.Respond(&model.GetStageResultResponse{GraphId: msg.GraphId, StageId: msg.StageId, Result: result})

	case *model.CompleteDelayStageRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Completing delayed stage")

	case *model.FaasInvocationResponse:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Received fn invocation response")

	default:
		log.Infof("Ignoring message of unknown type %v", reflect.TypeOf(msg))
	}
}

func (g *graphActor) Receive(context actor.Context) {
	if g.Recovering() {
		g.receiveRecover(context)
	} else {
		g.receiveStandard(context)
	}
}