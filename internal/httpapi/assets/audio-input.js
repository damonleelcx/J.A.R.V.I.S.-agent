/* FORGE audio input — choosing a microphone and asking for it properly.
 *
 * PRD AUD-03: "Mic selection, echo cancellation, noise suppression, speaker
 * separation, domain vocabulary, unit-aware transcription, push-to-talk/wake."
 * This file is the capture half of that list.
 *
 * # Why this exists rather than a getUserMedia call at each call site
 *
 * There are two of them — the workbench's level meter and the room client — and
 * the constraints below are not cosmetic. `{audio: true}` asks for a microphone
 * and accepts whatever the browser feels like: on most platforms that means echo
 * cancellation is ON, which is right, but it is not GUARANTEED, and gain control
 * and noise suppression vary by browser and by device. Two call sites asking two
 * different ways would eventually mean the same person sounds different
 * depending on which part of the product opened their microphone.
 *
 * # Why echo cancellation is not optional
 *
 * In a room, every participant is playing everybody else's audio through
 * speakers that their own microphone can hear. Without cancellation each speaker
 * feeds back into the mix, and — because the server transcribes each stream
 * separately — the transcript fills with each person repeating what the others
 * just said, attributed to the wrong speaker. It is not an audio-quality setting
 * here; it is what keeps attribution true.
 */
(function (global) {
  'use strict';

  var media = global.navigator && global.navigator.mediaDevices;

  /* constraints is what FORGE always asks a microphone for.
   *
   * Stated explicitly rather than left to the browser, so the same request is
   * made everywhere and a change is made in one place. `ideal` rather than
   * `exact`: a device that cannot do one of these should still be usable, and
   * `exact` would fail the whole request over a nicety. */
  function constraints(deviceId) {
    var audio = {
      echoCancellation: { ideal: true },
      noiseSuppression: { ideal: true },
      autoGainControl: { ideal: true },
      // Opus encodes at 48k internally and the SFU forwards it untouched; asking
      // for it here avoids a resample the browser would otherwise do twice.
      sampleRate: { ideal: 48000 },
      channelCount: { ideal: 1 }
    };
    if (deviceId) audio.deviceId = { exact: deviceId };
    return { audio: audio, video: false };
  }

  /* open acquires the microphone. Resolves with a MediaStream. */
  function open(deviceId) {
    if (!media || !media.getUserMedia) {
      return Promise.reject(new Error(
        'This browser cannot open a microphone. Everything here also works by typing.'));
    }
    return media.getUserMedia(constraints(deviceId));
  }

  /* devices lists the microphones this browser will admit to.
   *
   * # Why it may return unlabelled entries
   *
   * Before permission is granted, browsers report the DEVICES but not their
   * NAMES — otherwise the list itself would be a fingerprinting surface. So a
   * picker shown before the first grant says "Microphone 1", which is useless,
   * and one shown after says "MacBook Pro Microphone", which is not. Callers
   * should list after opening once; `labelled` reports which case this is so the
   * UI can say why rather than showing a mystery list. */
  function devices() {
    if (!media || !media.enumerateDevices) return Promise.resolve([]);
    return media.enumerateDevices().then(function (all) {
      return all.filter(function (d) { return d.kind === 'audioinput'; })
        .map(function (d, i) {
          return {
            id: d.deviceId,
            label: d.label || ('Microphone ' + (i + 1)),
            labelled: !!d.label
          };
        });
    }).catch(function () { return []; });
  }

  /* applied reports what the browser ACTUALLY gave us.
   *
   * Asked for is not the same as granted. A room that claims echo cancellation
   * while the device silently declined it would be telling somebody their
   * microphone is doing something it is not — and the consequence (see the file
   * header) is a corrupted transcript, not merely worse audio. The room surfaces
   * this rather than assuming the request was honoured. */
  function applied(stream) {
    var track = stream && stream.getAudioTracks && stream.getAudioTracks()[0];
    if (!track || !track.getSettings) return null;
    var s = track.getSettings();
    return {
      device: s.deviceId || '',
      label: track.label || '',
      echoCancellation: s.echoCancellation,
      noiseSuppression: s.noiseSuppression,
      autoGainControl: s.autoGainControl,
      sampleRate: s.sampleRate,
      channelCount: s.channelCount
    };
  }

  /* concerns names anything the browser declined, in words a person can act on.
   * Empty means everything asked for was granted. */
  function concerns(settings) {
    if (!settings) return [];
    var out = [];
    if (settings.echoCancellation === false) {
      out.push('Echo cancellation is off on this microphone. Others will hear ' +
               'themselves back, and the transcript may repeat what they said.');
    }
    if (settings.noiseSuppression === false) {
      out.push('Noise suppression is off on this microphone.');
    }
    return out;
  }

  /* stop releases the microphone.
   *
   * Explicit rather than left to garbage collection: the operating system's
   * recording indicator stays lit until every track is stopped, and an indicator
   * that stays on after somebody left a room is the kind of thing that destroys
   * trust in every other privacy claim the product makes. */
  function stop(stream) {
    if (!stream || !stream.getTracks) return;
    stream.getTracks().forEach(function (t) { t.stop(); });
  }

  global.ForgeAudioInput = {
    open: open,
    devices: devices,
    applied: applied,
    concerns: concerns,
    stop: stop,
    constraints: constraints,
    supported: !!(media && media.getUserMedia)
  };
})(window);
