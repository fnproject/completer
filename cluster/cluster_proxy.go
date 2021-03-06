package cluster

import (
	"golang.org/x/net/context"
	"github.com/fnproject/flow/model"
	"github.com/google/uuid"
)

type clusterProxy struct {
	manager *Manager
}

// NewClusterProxy creates a proxy service that forwards requests to the appropriate nodes based on the request shard
func NewClusterProxy(manager *Manager) model.FlowServiceServer {
	return &clusterProxy{manager: manager}
}

func (c *clusterProxy) CreateGraph(ctx context.Context, r *model.CreateGraphRequest) (*model.CreateGraphResponse, error) {
	flowID, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	r.FlowId = flowID.String()
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}

	log.Debug("Proxying create")
	return client.CreateGraph(ctx, r)
}

func (c *clusterProxy) AddStage(ctx context.Context, r *model.AddStageRequest) (*model.AddStageResponse, error) {
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}
	return client.AddStage(ctx, r)
}

func (c *clusterProxy) AddValueStage(ctx context.Context, r *model.AddCompletedValueStageRequest) (*model.AddStageResponse, error) {
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}
	return client.AddValueStage(ctx,r)

}


func (c *clusterProxy) AddInvokeFunction(ctx context.Context, r *model.AddInvokeFunctionStageRequest) (*model.AddStageResponse, error) {
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}
	return client.AddInvokeFunction(ctx, r)
}

func (c *clusterProxy) AddDelay(ctx context.Context, r *model.AddDelayStageRequest) (*model.AddStageResponse, error) {
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}
	return client.AddDelay(ctx, r)
}

func (c *clusterProxy) AwaitStageResult(ctx context.Context, r *model.AwaitStageResultRequest) (*model.AwaitStageResultResponse, error) {
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}
	return client.AwaitStageResult(ctx, r)
}

func (c *clusterProxy) CompleteStageExternally(ctx context.Context, r *model.CompleteStageExternallyRequest) (*model.CompleteStageExternallyResponse, error) {
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}
	return client.CompleteStageExternally(ctx, r)
}

func (c *clusterProxy) Commit(ctx context.Context, r *model.CommitGraphRequest) (*model.GraphRequestProcessedResponse, error) {
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}
	return client.Commit(ctx, r)
}

func (c *clusterProxy) GetGraphState(ctx context.Context, r *model.GetGraphStateRequest) (*model.GetGraphStateResponse, error) {
	client, err := c.manager.GetClient(r.FlowId)
	if err != nil {
		return nil, err
	}
	log.Debug("Getting graph state")
	return client.GetGraphState(ctx, r)
}

func (c *clusterProxy) StreamLifecycle(lr *model.StreamLifecycleRequest, stream model.FlowService_StreamLifecycleServer) error {
	log.Debug("Streaming lifecycle events")
	clients, err := c.manager.GetClients()
	if err != nil {
		return err
	}

	done := make(chan struct{})
	defer close(done)

	events := make(chan *model.GraphLifecycleEvent)
	errors := make(chan error, len(clients))

	for _, cl := range clients {

		client:= cl
		go func() {
			inStream, err := client.StreamLifecycle(stream.Context(), lr)
			if err != nil {
				errors <- err
				return
			}
			for {
				event, err := inStream.Recv()
				if err != nil {
					errors <- err
					return
				}
				select {
				case <-done:
					return
				case events<-event:
				}
			}
		}()
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case err := <-errors:
			return err
		case event := <-events:
			err := stream.Send(event)
			if err != nil {
				return err
			}
		}
	}
}

func (c *clusterProxy) StreamEvents(gr *model.StreamGraphRequest, stream model.FlowService_StreamEventsServer) error {
	client, err := c.manager.GetClient(gr.FlowId)

	if err != nil {
		return err
	}
	log.Debug("Getting graph events")
	remoteStream, err := client.StreamEvents(stream.Context(), gr)
	if err != nil {
		return err
	}
	for {
		event, err := remoteStream.Recv()
		if err != nil {
			return err
		}
		err = stream.Send(event)
		if err != nil {
			return err
		}
		log.Debug("cluster_proxy passed through one event", event)
	}
}
