package output

import (
	"fmt"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/pipeline/params"
	"github.com/livekit/protocol/utils"
	"github.com/tinyzimmer/go-gst/gst"
)

func buildFileStreamOutputBin(p *params.Params) (*OutputBin, error) {

	//we first need to link the audio and video pipelines to their respective tees, those tees need to be connected to the muxers,
	//and then those muxers need to be connected with the output bin, our output bin will have two sink ghost pads in case of file and stream.

	//filesink for file output
	filesink, err := gst.NewElement("filesink")
	if err != nil {
		return nil, err
	}
	if err = filesink.SetProperty("location", p.LocalFilepath); err != nil {
		return nil, err
	}
	if err = filesink.SetProperty("sync", false); err != nil {
		return nil, err
	}

	//tee for streaming to multiple endpoints
	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("output")
	if err = bin.AddMany(filesink, tee); err != nil {
		return nil, err
	}

	b := &OutputBin{
		bin:      bin,
		protocol: p.OutputType,
		tee:      tee,
		sinks:    make(map[string]*streamSink),
		logger:   p.Logger,
	}

	for _, url := range p.StreamUrls {
		sink, err := buildStreamSinkFS(b.protocol, url)
		if err != nil {
			return nil, err
		}

		if err = bin.AddMany(sink.queue, sink.sink); err != nil {
			return nil, err
		}

		b.sinks[url] = sink
	}

	// adding the ghost pads
	ghostPadRtmp := gst.NewGhostPad("rtmpsink", tee.GetStaticPad("sink"))
	ghostPadFile := gst.NewGhostPad("mp4sink", filesink.GetStaticPad("sink"))
	if !bin.AddPad(ghostPadRtmp.Pad) {
		return nil, errors.ErrGhostPadFailed
	}
	if !bin.AddPad(ghostPadFile.Pad) {
		return nil, errors.ErrGhostPadFailed
	}
	return b, nil
}

func buildStreamSinkFS(protocol params.OutputType, url string) (*streamSink, error) {
	id := utils.NewGuid("")

	queue, err := gst.NewElementWithName("queue", fmt.Sprintf("queue_%s", id))
	if err != nil {
		return nil, err
	}
	queue.SetArg("leaky", "downstream")

	var sink *gst.Element
	switch protocol {
	case params.OutputTypeFS:
		sink, err = gst.NewElementWithName("rtmp2sink", fmt.Sprintf("sink_%s", id))
		if err != nil {
			return nil, err
		}
		if err = sink.SetProperty("sync", false); err != nil {
			return nil, err
		}
		if err = sink.Set("location", url); err != nil {
			return nil, err
		}
	}

	return &streamSink{
		queue: queue,
		sink:  sink,
	}, nil
}
