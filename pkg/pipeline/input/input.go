package input

import (
	"context"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/carbonteq/egress-ehancement/pkg/config"
	"github.com/carbonteq/egress-ehancement/pkg/errors"
	"github.com/carbonteq/egress-ehancement/pkg/pipeline/input/sdk"
	"github.com/carbonteq/egress-ehancement/pkg/pipeline/input/web"
	"github.com/carbonteq/egress-ehancement/pkg/pipeline/params"
	"github.com/carbonteq/protocol/livekit"
)

type Input interface {
	Bin() *gst.Bin
	Element() *gst.Element
	Link() error
	StartRecording() chan struct{}
	EndRecording() chan struct{}
	Close()
}

func New(ctx context.Context, conf *config.Config, p *params.Params) (Input, error) {
	switch p.Info.Request.(type) {
	case *livekit.EgressInfo_RoomComposite,
		*livekit.EgressInfo_Web:
		return web.NewWebInput(ctx, conf, p)

	case *livekit.EgressInfo_TrackComposite,
		*livekit.EgressInfo_Track:
		return sdk.NewSDKInput(ctx, p)

	default:
		return nil, errors.ErrInvalidInput("request")
	}
}
