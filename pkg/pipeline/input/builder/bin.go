package builder

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/carbonteq/egress-ehancement/pkg/errors"
	"github.com/carbonteq/egress-ehancement/pkg/pipeline/params"
	"github.com/carbonteq/protocol/tracer"
)

const latency = uint64(41e8) // slightly larger than max audio latency

type InputBin struct {
	bin *gst.Bin

	audio    *AudioInput
	audioPad *gst.Pad

	video    *VideoInput
	videoPad *gst.Pad

	multiQueue *gst.Element
	mux        []*gst.Element // we change it to slice of mux in case of file and stream output
}

func NewWebInput(ctx context.Context, p *params.Params) (*InputBin, error) {
	input := &InputBin{
		bin: gst.NewBin("input"),
	}

	if p.AudioEnabled {
		audio, err := NewWebAudioInput(p)
		if err != nil {
			return nil, err
		}
		input.audio = audio
	}

	if p.VideoEnabled {
		video, err := NewWebVideoInput(p)
		if err != nil {
			return nil, err
		}
		input.video = video
	}

	if err := input.build(ctx, p); err != nil {
		return nil, err
	}

	return input, nil
}

func NewSDKInput(ctx context.Context, p *params.Params, audioSrc, videoSrc *app.Source, audioCodec, videoCodec webrtc.RTPCodecParameters) (*InputBin, error) {
	input := &InputBin{
		bin: gst.NewBin("input"),
	}

	fmt.Println("Inside the NewSDKInput")

	if p.AudioEnabled {
		audio, err := NewSDKAudioInput(p, audioSrc, audioCodec)
		if err != nil {
			return nil, err
		}
		input.audio = audio
	}

	if p.VideoEnabled {
		video, err := NewSDKVideoInput(p, videoSrc, videoCodec)
		if err != nil {
			return nil, err
		}
		input.video = video
	}

	if err := input.build(ctx, p); err != nil {
		return nil, err
	}

	return input, nil
}

func (b *InputBin) build(ctx context.Context, p *params.Params) error {
	ctx, span := tracer.Start(ctx, "Input.build")
	defer span.End()

	var err error
	// add audio to bin
	if b.audio != nil {
		if err = b.audio.AddToBin(b.bin); err != nil {
			return err
		}
	}

	// add video to bin
	if b.video != nil {
		if err = b.video.AddToBin(b.bin); err != nil {
			return err
		}
	}

	// queue
	b.multiQueue, err = gst.NewElement("multiqueue")
	if err != nil {
		return err
	}
	if err = b.bin.Add(b.multiQueue); err != nil {
		return err
	}

	// mux
	b.mux, err = buildMux(p)
	if err != nil {
		return err
	}
	if b.mux != nil {
		if err = b.bin.AddMany(b.mux...); err != nil { //change from Add to AddMany
			return err
		}
	}

	// HLS has no output bin
	if p.OutputType == params.OutputTypeHLS {
		return nil
	}

	// create ghost pad
	var ghostPad *gst.GhostPad

	// new ghost pads in case of file + stream output
	var ghostPadflv *gst.GhostPad
	var ghostPadmp4 *gst.GhostPad

	//so our input bin will have two source pads in case of file + stream output
	if len(b.mux) == 2 {
		ghostPadflv = gst.NewGhostPad("flvsrc", b.mux[0].GetStaticPad("src"))
		ghostPadmp4 = gst.NewGhostPad("mp4src", b.mux[1].GetStaticPad("src"))
		fmt.Println("length of mux was 2 so made the ghost pads")
	} else if b.mux != nil {
		ghostPad = gst.NewGhostPad("src", b.mux[0].GetStaticPad("src"))
	} else if b.audio != nil {
		b.audioPad = b.multiQueue.GetRequestPad("sink_%u")
		ghostPad = gst.NewGhostPad("src", b.multiQueue.GetStaticPad("src_0"))
	} else if b.video != nil {
		b.videoPad = b.multiQueue.GetRequestPad("sink_%u")
		ghostPad = gst.NewGhostPad("src", b.multiQueue.GetStaticPad("src_0"))
	}

	fmt.Println("Normal ghost pad should be nil: ", ghostPad)
	fmt.Println("Ghost Pad FLV: ", ghostPadflv)
	fmt.Println("Ghost Pad MP4: ", ghostPadmp4)

	// adding a new if statement for our file and stream type
	if p.EgressType == params.EgressTypeFileAndStream {
		if ghostPadflv == nil || !b.bin.AddPad(ghostPadflv.Pad) {
			fmt.Println("First if: flvpad false ")
			return errors.ErrGhostPadFailed
		}
		if ghostPadmp4 == nil || !b.bin.AddPad(ghostPadmp4.Pad) {
			fmt.Println("Second if: mp4pad false ")
			return errors.ErrGhostPadFailed
		}

	} else if ghostPad == nil || !b.bin.AddPad(ghostPad.Pad) {
		fmt.Println("Third if: normalpad false ")
		return errors.ErrGhostPadFailed
	}
	fmt.Println("Yay error was nil so returning nil, ghost pads added sucessfully")

	return nil
}

func (b *InputBin) Bin() *gst.Bin {
	return b.bin
}

func (b *InputBin) Element() *gst.Element {
	return b.bin.Element
}

func (b *InputBin) Link() error {
	mqPad := 0

	// link audio elements
	if b.audio != nil {
		if err := b.audio.Link(); err != nil {
			return err
		}

		// adding a new if statement for our stream + file output
		if len(b.mux) == 2 {
			// requesting sink pads from multiqueue
			queuePadflv := b.multiQueue.GetRequestPad("sink_%u")
			queuePadmp4 := b.multiQueue.GetRequestPad("sink_%u")

			//requesting the audio tee
			audioTee := b.audio.tee

			//requesting the source pads of tee
			teePadflv := audioTee.GetRequestPad("src_%u")
			teePadmp4 := audioTee.GetRequestPad("src_%u")
			//linking the tee source pads with the multiqueue sink pads
			if linkReturn := teePadflv.Link(queuePadflv); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("tee pad flv", "queuePadflv", linkReturn.String())
			}
			if linkReturn := teePadmp4.Link(queuePadmp4); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("tee pad mp4", "queuePadmp4", linkReturn.String())
			}

			//now lets link the multiqueue source pads with the mux audio pads
			muxAudioPadflv := b.mux[0].GetRequestPad("audio")
			muxAudioPadmp4 := b.mux[1].GetRequestPad("audio_%u")

			//linking the multiqueue source pads with the audio sink pads of flv and mp4 mux
			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxAudioPadflv); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("queuePadflv", "muxAudioPadflv", linkReturn.String())
			}
			mqPad++
			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxAudioPadmp4); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("queuePadmp4", "muxAudioPadmp4", linkReturn.String())
			}
			mqPad++

		} else {
			queuePad := b.audioPad
			if queuePad == nil {
				queuePad = b.multiQueue.GetRequestPad("sink_%u")
			}

			if linkReturn := b.audio.GetSrcPad().Link(queuePad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("audio", "multiQueue", linkReturn.String())
			}

			if b.mux != nil {
				// Different muxers use different pad naming
				muxAudioPad := b.mux[0].GetRequestPad("audio")
				if muxAudioPad == nil {
					muxAudioPad = b.mux[0].GetRequestPad("audio_%u")
				}
				if muxAudioPad == nil {
					return errors.New("no audio pad found")
				}

				if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxAudioPad); linkReturn != gst.PadLinkOK {
					return errors.ErrPadLinkFailed("audio", "mux", linkReturn.String())
				}
			}

			mqPad++
		}
	}

	// link video elements
	if b.video != nil {
		if err := b.video.Link(); err != nil {
			return err
		}

		//adding a new if statement for our stream + file output
		if len(b.mux) == 2 {
			//requesting sink pads from multiqueue
			queuePadflv := b.multiQueue.GetRequestPad("sink_%u")
			queuePadmp4 := b.multiQueue.GetRequestPad("sink_%u")

			//requesting the last element (which is a tee) from our audioElements slice
			videoTee := b.video.elements[len(b.video.elements)-1]

			//requesting the source pads of tee
			teePadflv := videoTee.GetRequestPad("src_%u")
			teePadmp4 := videoTee.GetRequestPad("src_%u")

			//linking the tee source pads with the multiqueue sink pads
			if linkReturn := teePadflv.Link(queuePadflv); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("teePadflv", "queuePadflv", linkReturn.String())
			}
			if linkReturn := teePadmp4.Link(queuePadmp4); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("teePadmp4", "queuePadmp4", linkReturn.String())
			}

			//now lets link the multiqueue source pads with the mux audio pads
			muxVideoPadflv := b.mux[0].GetRequestPad("video")
			muxVideoPadmp4 := b.mux[1].GetRequestPad("video_%u")

			//linking the multiqueue source pads with the audio sink pads of flv and mp4 mux
			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxVideoPadflv); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("queuePadflv", "muxVideoPadflv", linkReturn.String())
			}
			mqPad++
			if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxVideoPadmp4); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("queuePadmp4", "muxVideoPadmp4", linkReturn.String())
			}
			mqPad++

		} else {
			queuePad := b.videoPad
			if queuePad == nil {
				queuePad = b.multiQueue.GetRequestPad("sink_%u")
			}

			if linkReturn := b.video.GetSrcPad().Link(queuePad); linkReturn != gst.PadLinkOK {
				return errors.ErrPadLinkFailed("video", "multiQueue", linkReturn.String())
			}

			if b.mux != nil {
				// Different muxers use different pad naming
				muxVideoPad := b.mux[0].GetRequestPad("video")
				if muxVideoPad == nil {
					muxVideoPad = b.mux[0].GetRequestPad("video_%u")
				}
				if muxVideoPad == nil {
					return errors.New("no video pad found")
				}

				if linkReturn := b.multiQueue.GetStaticPad(fmt.Sprintf("src_%d", mqPad)).Link(muxVideoPad); linkReturn != gst.PadLinkOK {
					return errors.ErrPadLinkFailed("video", "mux", linkReturn.String())
				}
			}
		}
	}

	return nil
}

func buildQueue() (*gst.Element, error) {
	queue, err := gst.NewElement("queue")
	if err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-time", latency); err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-bytes", uint(0)); err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-buffers", uint(0)); err != nil {
		return nil, err
	}
	return queue, nil
}

func buildMux(p *params.Params) ([]*gst.Element, error) {
	switch p.OutputType {
	case params.OutputTypeRaw:
		return nil, nil

	case params.OutputTypeOGG:
		oggmux, err := gst.NewElement("oggmux")
		if err != nil {
			return nil, err
		} else {
			return []*gst.Element{oggmux}, nil
		}

	case params.OutputTypeIVF:
		avmux, err := gst.NewElement("avmux_ivf")
		if err != nil {
			return nil, err
		} else {
			return []*gst.Element{avmux}, nil
		}

	case params.OutputTypeMP4:
		mp4mux, err := gst.NewElement("mp4mux")
		if err != nil {
			return nil, err
		} else {
			return []*gst.Element{mp4mux}, nil
		}

	case params.OutputTypeTS:
		mpegtsmux, err := gst.NewElement("mpegtsmux")
		if err != nil {
			return nil, err
		} else {
			return []*gst.Element{mpegtsmux}, nil
		}

	case params.OutputTypeWebM:
		webmmux, err := gst.NewElement("webmmux")
		if err != nil {
			return nil, err
		} else {
			return []*gst.Element{webmmux}, nil
		}

	case params.OutputTypeRTMP:
		if p.EgressType == params.EgressTypeFileAndStream {
			flvmux, err := gst.NewElement("flvmux")
			if err != nil {
				return nil, err
			}
			if err = flvmux.SetProperty("streamable", true); err != nil {
				return nil, err
			}
			mp4mux, err := gst.NewElement("mp4mux")
			if err != nil {
				return nil, err
			} else {
				return []*gst.Element{flvmux, mp4mux}, nil
			}
		}
		flvmux, err := gst.NewElement("flvmux")
		if err != nil {
			return nil, err
		}
		if err = flvmux.SetProperty("streamable", true); err != nil {
			return nil, err
		}
		return []*gst.Element{flvmux}, nil

	case params.OutputTypeHLS:
		splitmuxsink, err := gst.NewElement("splitmuxsink")
		if err != nil {
			return nil, err
		}
		if err = splitmuxsink.SetProperty("max-size-time", uint64(time.Duration(p.SegmentDuration)*time.Second)); err != nil {
			return nil, err
		}
		if err = splitmuxsink.SetProperty("async-finalize", true); err != nil {
			return nil, err
		}
		if err = splitmuxsink.SetProperty("muxer-factory", "mpegtsmux"); err != nil {
			return nil, err
		}
		if err = splitmuxsink.SetProperty("location", fmt.Sprintf("%s_%%05d.ts", p.LocalFilePrefix)); err != nil {
			return nil, err
		}
		return []*gst.Element{splitmuxsink}, nil

	default:
		return nil, errors.ErrInvalidInput("output type")
	}
}

func getSrcPad(elements []*gst.Element) *gst.Pad {
	return elements[len(elements)-1].GetStaticPad("src")
}
