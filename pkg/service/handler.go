package service

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/abdulhaseeb08/egress-ehancement/pkg/config"
	"github.com/abdulhaseeb08/egress-ehancement/pkg/errors"
	"github.com/abdulhaseeb08/egress-ehancement/pkg/pipeline"
	"github.com/abdulhaseeb08/egress-ehancement/pkg/pipeline/params"
	"github.com/abdulhaseeb08/protocol/egress"
	"github.com/abdulhaseeb08/protocol/livekit"
	"github.com/abdulhaseeb08/protocol/logger"
	"github.com/abdulhaseeb08/protocol/tracer"
)

type Handler struct {
	conf      *config.Config
	rpcServer egress.RPCServer
	kill      chan struct{}
}

func NewHandler(conf *config.Config, rpcServer egress.RPCServer) *Handler {
	return &Handler{
		conf:      conf,
		rpcServer: rpcServer,
		kill:      make(chan struct{}),
	}
}

func (h *Handler) HandleRequest(ctx context.Context, req *livekit.StartEgressRequest) {
	ctx, span := tracer.Start(ctx, "Handler.HandleRequest")
	defer span.End()

	p, err := h.buildPipeline(ctx, req)
	if err != nil {
		span.RecordError(err)
		return
	}

	// subscribe to request channel
	requests, err := h.rpcServer.EgressSubscription(context.Background(), p.GetInfo().EgressId)
	if err != nil {
		span.RecordError(err)
		return
	}
	defer func() {
		err := requests.Close()
		if err != nil {
			logger.Errorw("failed to unsubscribe from request channel", err)
		}
	}()

	// start egress
	result := make(chan *livekit.EgressInfo, 1)
	go func() {
		result <- p.Run(ctx)
	}()

	for {
		select {
		case <-h.kill:
			// kill signal received
			p.SendEOS(ctx)

		case res := <-result:
			// recording finished
			h.sendUpdate(ctx, res)
			return

		case msg := <-requests.Channel():
			// request received
			request := &livekit.EgressRequest{}
			err = proto.Unmarshal(requests.Payload(msg), request)
			if err != nil {
				logger.Errorw("failed to read request", err, "egressID", p.GetInfo().EgressId)
				continue
			}
			logger.Debugw("handling request", "egressID", p.GetInfo().EgressId, "requestID", request.RequestId)

			switch r := request.Request.(type) {
			case *livekit.EgressRequest_UpdateStream:
				err = p.UpdateStream(ctx, r.UpdateStream)
			case *livekit.EgressRequest_Stop:
				p.SendEOS(ctx)
			default:
				err = errors.ErrInvalidRPC
			}

			h.sendResponse(ctx, request, p.GetInfo(), err)
		}
	}
}

func (h *Handler) buildPipeline(ctx context.Context, req *livekit.StartEgressRequest) (*pipeline.Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Handler.buildPipeline")
	defer span.End()

	// build/verify params
	pipelineParams, err := params.GetPipelineParams(ctx, h.conf, req)
	var p *pipeline.Pipeline

	if err == nil {
		// create the pipeline
		p, err = pipeline.New(ctx, h.conf, pipelineParams)
	}

	if err != nil {
		info := pipelineParams.Info
		info.Error = err.Error()
		info.Status = livekit.EgressStatus_EGRESS_FAILED
		h.sendUpdate(ctx, info)
		return nil, err
	}

	p.OnStatusUpdate(h.sendUpdate)
	return p, nil
}

func (h *Handler) sendUpdate(ctx context.Context, info *livekit.EgressInfo) {
	requestType, outputType := getTypes(info)
	switch info.Status {
	case livekit.EgressStatus_EGRESS_FAILED:
		logger.Warnw("egress failed", errors.New(info.Error),
			"egressID", info.EgressId,
			"request_type", requestType,
			"output_type", outputType,
		)
	case livekit.EgressStatus_EGRESS_COMPLETE:
		logger.Infow("egress completed",
			"egressID", info.EgressId,
			"request_type", requestType,
			"output_type", outputType,
		)
	default:
		logger.Infow("egress updated",
			"egressID", info.EgressId,
			"request_type", requestType,
			"output_type", outputType,
			"status", info.Status,
		)
	}

	if err := h.rpcServer.SendUpdate(ctx, info); err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (h *Handler) sendResponse(ctx context.Context, req *livekit.EgressRequest, info *livekit.EgressInfo, err error) {
	args := []interface{}{
		"egressID", info.EgressId,
		"requestID", req.RequestId,
		"senderID", req.SenderId,
	}

	if err != nil {
		logger.Warnw("request failed", err, args...)
	} else {
		logger.Debugw("request handled", args...)
	}

	if err := h.rpcServer.SendResponse(ctx, req, info, err); err != nil {
		logger.Errorw("failed to send response", err, args...)
	}
}

func (h *Handler) Kill() {
	select {
	case <-h.kill:
		return
	default:
		close(h.kill)
	}
}

func getTypes(info *livekit.EgressInfo) (requestType string, outputType string) {
	switch req := info.Request.(type) {
	case *livekit.EgressInfo_RoomComposite:
		requestType = "room_composite"
		switch req.RoomComposite.Output.(type) {
		case *livekit.RoomCompositeEgressRequest_File:
			outputType = "file"
		case *livekit.RoomCompositeEgressRequest_Stream:
			outputType = "stream"
		case *livekit.RoomCompositeEgressRequest_Segments:
			outputType = "segments"
		}
	case *livekit.EgressInfo_Web:
		requestType = "web"
		switch req.Web.Output.(type) {
		case *livekit.WebEgressRequest_File:
			outputType = "file"
		case *livekit.WebEgressRequest_Stream:
			outputType = "stream"
		case *livekit.WebEgressRequest_Segments:
			outputType = "segments"
		}
	case *livekit.EgressInfo_TrackComposite:
		requestType = "track_composite"
		switch req.TrackComposite.Output.(type) {
		case *livekit.TrackCompositeEgressRequest_File:
			outputType = "file"
		case *livekit.TrackCompositeEgressRequest_Stream:
			outputType = "stream"
		case *livekit.TrackCompositeEgressRequest_Segments:
			outputType = "segments"
		}
	case *livekit.EgressInfo_Track:
		requestType = "track"
		switch req.Track.Output.(type) {
		case *livekit.TrackEgressRequest_File:
			outputType = "file"
		case *livekit.TrackEgressRequest_WebsocketUrl:
			outputType = "websocket"
		}
	}

	return
}
