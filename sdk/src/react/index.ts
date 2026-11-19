import { useEffect, useMemo, useRef, useState } from "react";
import type { WaCallsCall } from "../call.js";
import { WaCallsClient } from "../client.js";
import type { CallSoundsOptions } from "../sounds.js";
import type { CallState, WaCallsClientOptions } from "../types.js";

/**
 * A client tied to the component's lifetime.
 *
 * It is rebuilt only when the connection identity changes, so a re-render never
 * drops the event stream. `connectEvents` is on by default because incoming
 * calls and pairing QR codes exist nowhere else. `sounds` turns on the call
 * progress tones (ringback, ringtone, answer and hang-up beeps); off by default.
 */
export const useWaCallsClient = (
  options: WaCallsClientOptions,
  {
    connectEvents = true,
    sounds = false,
  }: { connectEvents?: boolean; sounds?: boolean | CallSoundsOptions } = {},
) => {
  const { baseUrl, token, clientId, storageKey } = options;

  const client = useMemo(
    () => new WaCallsClient({ baseUrl, token, clientId, storageKey }),
    [baseUrl, token, clientId, storageKey],
  );

  useEffect(() => {
    if (!connectEvents) return;
    client.events.connect();
    return () => client.events.close();
  }, [client, connectEvents]);

  // Keyed on the serialized options so an inline object literal does not
  // rebuild the tones on every render.
  const soundsKey = sounds === false ? "" : JSON.stringify(sounds === true ? {} : sounds);
  useEffect(() => {
    if (!soundsKey) return;
    return client.enableSounds(JSON.parse(soundsKey) as CallSoundsOptions);
  }, [client, soundsKey]);

  return client;
};

/** Re-renders the component as the audio leg moves through its states. */
export const useCallState = (call: WaCallsCall | null): CallState => {
  const [state, setState] = useState<CallState>(call?.state ?? "idle");

  useEffect(() => {
    if (!call) {
      setState("idle");
      return;
    }
    setState(call.state);
    return call.on("state", setState);
  }, [call]);

  return state;
};

/**
 * Wires the call's audio into an `<audio>` element.
 *
 * Attach the returned ref to an element that is in the DOM — `<audio ref={ref}
 * autoPlay />` with no controls is enough. Nothing is heard until something
 * plays this stream.
 */
export const useRemoteAudio = (call: WaCallsCall | null) => {
  const ref = useRef<HTMLAudioElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.srcObject = call?.remoteStream ?? null;
    if (call?.remoteStream) {
      // Autoplay policy: this resolves once the user gesture that started the
      // call has unlocked playback, and rejects harmlessly otherwise.
      void el.play().catch(() => {});
    }
    return () => {
      el.srcObject = null;
    };
  }, [call, call?.remoteStream]);

  return ref;
};
