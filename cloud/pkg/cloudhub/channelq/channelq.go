package channelq

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"k8s.io/klog"

	beehiveContext "github.com/kubeedge/beehive/pkg/core/context"
	"github.com/kubeedge/kubeedge/cloud/pkg/cloudhub/common/model"
)

// Read channel buffer size
const (
	rChanBufSize = 10
)

// EventSet holds a set of events
type EventSet interface {
	Ack() error
	Get() (*model.Event, error)
}

// ChannelEventSet is the channel implementation of EventSet
type ChannelEventSet struct {
	current  model.Event
	messages <-chan model.Event
}

// NewChannelEventSet initializes a new ChannelEventSet instance
func NewChannelEventSet(messages <-chan model.Event) *ChannelEventSet {
	return &ChannelEventSet{messages: messages}
}

// Ack acknowledges once the event is processed
func (s *ChannelEventSet) Ack() error {
	return nil
}

// Get obtains one event from the queue
func (s *ChannelEventSet) Get() (*model.Event, error) {
	var ok bool
	s.current, ok = <-s.messages
	if !ok {
		return nil, fmt.Errorf("failed to get message from cluster, reason: channel is closed")
	}
	return &s.current, nil
}

// ChannelEventQueue is the channel implementation of EventQueue
type ChannelEventQueue struct {
	ctx         *beehiveContext.Context
	channelPool sync.Map
}

// NewChannelEventQueue initializes a new ChannelEventQueue
func NewChannelEventQueue(ctx *beehiveContext.Context) *ChannelEventQueue {
	q := ChannelEventQueue{ctx: ctx}
	return &q
}

// DispatchMessage gets the message from the cloud, extracts the
// node id from it, gets the channel associated with the node
// and pushes the event on the channel
func (q *ChannelEventQueue) DispatchMessage(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			klog.Warningf("Cloudhub channel eventqueue dispatch message loop stoped")
			return
		default:
		}
		msg, err := q.ctx.Receive(model.SrcCloudHub)
		if err != nil {
			klog.Info("receive not Message format message")
			continue
		}
		resource := msg.Router.Resource
		tokens := strings.Split(resource, "/")
		numOfTokens := len(tokens)
		var nodeID string
		for i, token := range tokens {
			if token == model.ResNode && i+1 < numOfTokens {
				nodeID = tokens[i+1]
				break
			}
		}
		if nodeID == "" {
			klog.Warning("node id is not found in the message")
			continue
		}
		rChannel, err := q.getRChannel(nodeID)
		if err != nil {
			klog.Infof("fail to get dispatch channel for %s", nodeID)
			continue
		}
		rChannel <- model.MessageToEvent(&msg)
	}
}

func (q *ChannelEventQueue) getRChannel(nodeID string) (chan model.Event, error) {
	channels, ok := q.channelPool.Load(nodeID)
	if !ok {
		klog.Errorf("rChannel for edge node %s is removed", nodeID)
		return nil, fmt.Errorf("rChannel not found")
	}
	rChannel := channels.(chan model.Event)
	return rChannel, nil
}

// Connect allocates rChannel for given project and group
func (q *ChannelEventQueue) Connect(info *model.HubInfo) error {
	_, ok := q.channelPool.Load(info.NodeID)
	if ok {
		return fmt.Errorf("edge node %s is already connected", info.NodeID)
	}
	// allocate a new rchannel with default buffer size
	rChannel := make(chan model.Event, rChanBufSize)
	_, ok = q.channelPool.LoadOrStore(info.NodeID, rChannel)
	if ok {
		// rchannel is already allocated
		return fmt.Errorf("edge node %s is already connected", info.NodeID)
	}
	return nil
}

// Close closes rChannel for given project and group
func (q *ChannelEventQueue) Close(info *model.HubInfo) error {
	channels, ok := q.channelPool.Load(info.NodeID)
	if !ok {
		klog.Warningf("rChannel for edge node %s is already removed", info.NodeID)
		return nil
	}
	rChannel := channels.(chan model.Event)
	close(rChannel)
	q.channelPool.Delete(info.NodeID)
	return nil
}

// Publish sends message via the rchannel to Edge Controller
func (q *ChannelEventQueue) Publish(info *model.HubInfo, event *model.Event) error {
	msg := model.EventToMessage(event)
	switch msg.Router.Source {
	case model.ResTwin:
		q.ctx.Send2Group(model.SrcDeviceController, msg)
	default:
		q.ctx.Send2Group(model.SrcEdgeController, msg)
	}
	return nil
}

// Consume retrieves message from the rChannel for given project and group
func (q *ChannelEventQueue) Consume(info *model.HubInfo) (EventSet, error) {
	rChannel, err := q.getRChannel(info.NodeID)
	if err != nil {
		return nil, err
	}
	return NewChannelEventSet((<-chan model.Event)(rChannel)), nil
}

// Workload returns the number of queue channels connected to queue
func (q *ChannelEventQueue) Workload() (float64, error) {
	return 1, nil
}
