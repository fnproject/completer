package actor

import (
	"reflect"

	"github.com/AsynkronIT/protoactor-go/actor"
	"github.com/fnproject/completer/model"
	"github.com/sirupsen/logrus"
)

var (
	log = logrus.WithField("logger", "actor")
)

type graphSupervisor struct {
}

func (s *graphSupervisor) Receive(context actor.Context) {
	switch msg := context.Message().(type) {

	case *model.CreateGraphRequest:
		props := actor.FromInstance(&graphActor{})
		child, err := context.SpawnNamed(props, msg.GraphId)
		if err != nil {
			log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Warn("Failed to spawn graph actor")
			return
		}
		child.Request(msg, context.Sender())

	default:
		isGraphMsg, graphId := getGraphId(msg)
		if !isGraphMsg {
			return
		}
		found, child := findChild(context, graphId)
		if !found {
			log.WithFields(logrus.Fields{"graph_id": graphId}).Warn("No child actor found")
			return
		}
		child.Request(msg, context.Sender())
	}
}

func getGraphId(msg interface{}) (bool, string) {
	graphId := reflect.ValueOf(msg).Elem().FieldByName("GraphId")
	if graphId.IsValid() {
		return true, graphId.String()
	}
	return false, ""
}

func findChild(context actor.Context, graphId string) (bool, *actor.PID) {
	fullId := context.Self().Id + "/" + graphId
	for _, pid := range context.Children() {
		if pid.Id == fullId {
			return true, pid
		}
	}
	return false, nil
}

type graphActor struct {
}

func (g *graphActor) Receive(context actor.Context) {
	switch msg := context.Message().(type) {

	case *model.CreateGraphRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Creating graph")
		context.Respond(&model.CreateGraphResponse{GraphId: msg.GraphId})

	case *model.AddChainedStageRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding chained stage")
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: "1"})

	case *model.AddCompletedValueStageRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding completed value stage")
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: "1"})

	case *model.AddDelayStageRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding delay stage")
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: "1"})

	case *model.AddExternalCompletionStageRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding external completion stage")
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: "1"})

	case *model.AddInvokeFunctionStageRequest:
		log.WithFields(logrus.Fields{"graph_id": msg.GraphId}).Debug("Adding invoke stage")
		context.Respond(&model.AddStageResponse{GraphId: msg.GraphId, StageId: "1"})

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
	}

}
