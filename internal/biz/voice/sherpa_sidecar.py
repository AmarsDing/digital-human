#!/usr/bin/env python3
"""NDJSON stdin/stdout adapter for sherpa-onnx online CPU recognition.

Protocol: begin / audio(base64 s16le PCM) / end / cancel, all scoped by turn_id.
stdout is protocol-only; model loading or inference failures are explicit error
messages, never fake text.
"""
import argparse
import base64
import json
import sys


def emit(cmd, **fields):
    sys.stdout.write(json.dumps({"cmd": cmd, **fields}, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-type", choices=["zipformer", "paraformer"], required=True)
    for name in ["tokens", "encoder", "decoder", "joiner", "threads"]:
        parser.add_argument("--" + name, required=name != "joiner")
    args = parser.parse_args()

    try:
        import numpy as np
        import sherpa_onnx

        common = dict(
            tokens=args.tokens,
            num_threads=int(args.threads),
            sample_rate=16000,
            feature_dim=80,
            provider="cpu",
            enable_endpoint_detection=True,
            rule1_min_trailing_silence=2.4,
            rule2_min_trailing_silence=1.2,
            rule3_min_utterance_length=20.0,
            decoding_method="greedy_search",
        )
        if args.model_type == "zipformer":
            recognizer = sherpa_onnx.OnlineRecognizer.from_transducer(
                encoder=args.encoder, decoder=args.decoder, joiner=args.joiner, **common
            )
        else:
            recognizer = sherpa_onnx.OnlineRecognizer.from_paraformer(
                encoder=args.encoder, decoder=args.decoder, **common
            )
    except Exception as exc:
        emit("error", code="asr_unavailable", message=str(exc))
        return 1

    emit("ready")
    stream = None
    turn_id = None
    try:
        for line in sys.stdin:
            try:
                msg = json.loads(line)
                cmd = msg.get("cmd")
                if cmd == "begin":
                    turn_id = msg.get("turn_id")
                    stream = recognizer.create_stream()
                elif cmd == "audio" and stream is not None and msg.get("turn_id") == turn_id:
                    pcm = base64.b64decode(msg["data"], validate=True)
                    samples = np.frombuffer(pcm, dtype=np.int16).astype(np.float32) / 32768.0
                    stream.accept_waveform(16000, samples)
                    while recognizer.is_ready(stream):
                        recognizer.decode_stream(stream)
                    text = recognizer.get_result(stream)
                    if text:
                        emit("partial", text=text, turn_id=turn_id)
                elif cmd == "end" and stream is not None and msg.get("turn_id") == turn_id:
                    stream.input_finished()
                    while recognizer.is_ready(stream):
                        recognizer.decode_stream(stream)
                    emit("final", text=recognizer.get_result(stream), turn_id=turn_id)
                    stream = None
                elif cmd == "cancel" and msg.get("turn_id") == turn_id:
                    stream = None
                    emit("cancelled", turn_id=turn_id)
            except Exception as exc:
                emit("error", code="asr_unavailable", message=str(exc), turn_id=turn_id)
                stream = None
    except Exception as exc:
        emit("error", code="asr_unavailable", message=str(exc), turn_id=turn_id)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
