package com.torrenttv.app.avplay

import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.view.SurfaceView
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.TrackSelectionOverride
import androidx.media3.exoplayer.ExoPlayer
import org.json.JSONArray
import org.json.JSONObject

/**
 * The native half of the AVPlay shape: receives the web app's player
 * commands through Bridge, drives one ExoPlayer on a SurfaceView behind the
 * WebView, and reports progress back as AVPlay listener events through
 * `dispatch` (Kotlin → FileListTVBridge.dispatch → JS listener).
 *
 * Everything stateful runs on the main thread; @JavascriptInterface calls
 * arrive on the WebView's binder thread and are posted. Subtitles render in
 * the page's HTML overlay from server-prepared VTT, so text tracks stay
 * disabled and the subtitle-path/position calls are accepted as no-ops.
 */
class AvPlayBridge(private val surface: SurfaceView, private val dispatch: (String) -> Unit) {
    private val main = Handler(Looper.getMainLooper())
    private var player: ExoPlayer? = null
    private var sourceUrl: String? = null
    private var successToken: String? = null
    private var errorToken: String? = null
    private var lastDurationMs = 0L
    private var buffering = false
    private var ticker: Runnable? = null

    // -- commands (called on the JS bridge thread) --
    fun open(url: String) { sourceUrl = url }

    fun setDisplayRect(x: Int, y: Int, width: Int, height: Int) { /* the surface fills the activity */ }

    fun setDisplayMethod(mode: String) {
        onMain {
            player?.videoScalingMode = when (mode) {
                "PLAYER_DISPLAY_MODE_FULL_SCREEN" -> C.VIDEO_SCALING_MODE_SCALE_TO_FIT_WITH_CROPPING
                else -> C.VIDEO_SCALING_MODE_SCALE_TO_FIT
            }
        }
    }

    fun prepareAsync(success: String, error: String) {
        onMain {
            successToken = success
            errorToken = error
            releasePlayer()
            val url = sourceUrl
            if (url.isNullOrEmpty()) {
                reportError("no source URL was opened")
                return@onMain
            }
            val exo = ExoPlayer.Builder(surface.context).build()
            player = exo
            exo.setVideoSurfaceView(surface)
            exo.addListener(object : Player.Listener {
                override fun onPlaybackStateChanged(state: Int) {
                    AvPlayStateMapper.bufferingCallback(state)?.let { name ->
                        if (state == Player.STATE_BUFFERING) {
                            buffering = true
                            dispatchEvent(name, listOf(0))
                        } else {
                            dispatchEvent(name)
                        }
                    }
                    if (state == Player.STATE_READY) {
                        buffering = false
                        lastDurationMs = exo.duration.coerceAtLeast(0L)
                        successToken?.let { token -> dispatchCallback(token, listOf(lastDurationMs)) }
                        successToken = null
                        errorToken = null
                        startTicker(exo)
                    }
                }

                override fun onPlayerError(error: PlaybackException) {
                    reportError(error.message ?: "playback error")
                }
            })
            exo.setMediaItem(MediaItem.fromUri(Uri.parse(url)))
            exo.prepare()
        }
    }

    fun play() = withPlayer { it.play() }

    fun pause() = withPlayer { it.pause() }

    fun seekTo(milliseconds: Double) = withPlayer { it.seekTo(milliseconds.toLong()) }

    fun stop() = withPlayer { it.stop() }

    fun close() {
        onMain { releasePlayer() }
    }

    fun getDuration(): Double = lastDurationMs.toDouble()

    fun getTotalTrackInfo(): String {
        val exo = player ?: return "[]"
        return TrackInfo.toJson(tracksOf(exo))
    }

    fun setSelectTrack(type: String, index: Int) {
        if (type != "AUDIO") return
        withPlayer { exo ->
            var audioIndex = 0
            for (group in exo.currentTracks.groups) {
                if (group.type != C.TRACK_TYPE_AUDIO) continue
                for (trackIndex in 0 until group.mediaTrackGroup.length) {
                    if (audioIndex == index) {
                        exo.trackSelectionParameters = exo.trackSelectionParameters.buildUpon()
                            .setOverrideForType(TrackSelectionOverride(group.mediaTrackGroup, trackIndex))
                            .build()
                        return@withPlayer
                    }
                    audioIndex++
                }
            }
        }
    }

    fun setSilentSubtitle(silent: Boolean) {
        onMain {
            val current = player ?: return@onMain
            current.trackSelectionParameters = current.trackSelectionParameters.buildUpon()
                .setTrackTypeDisabled(C.TRACK_TYPE_TEXT, silent)
                .build()
        }
    }

    // -- internals (main thread) --
    private fun tracksOf(exo: ExoPlayer): List<TrackInfo.Track> {
        val tracks = mutableListOf<TrackInfo.Track>()
        for (group in exo.currentTracks.groups) {
            val format = group.mediaTrackGroup.getFormat(0)
            val type = when (group.type) {
                C.TRACK_TYPE_AUDIO -> "AUDIO"
                C.TRACK_TYPE_TEXT -> "TEXT"
                C.TRACK_TYPE_VIDEO -> "VIDEO"
                else -> continue
            }
            tracks.add(TrackInfo.Track(type, format.language ?: "", format.sampleMimeType ?: ""))
        }
        return tracks
    }

    private fun startTicker(exo: ExoPlayer) {
        stopTicker()
        val runnable = object : Runnable {
            override fun run() {
                val current = player
                if (current == null || current !== exo) return
                dispatchEvent("oncurrentplaytime", listOf(current.currentPosition))
                if (buffering) dispatchEvent("onbufferingprogress", listOf((current.bufferedPercentage * 100).toInt()))
                main.postDelayed(this, 500)
            }
        }
        ticker = runnable
        main.postDelayed(runnable, 500)
    }

    private fun stopTicker() {
        ticker?.let { main.removeCallbacks(it) }
        ticker = null
    }

    private fun releasePlayer() {
        stopTicker()
        player?.release()
        player = null
    }

    private fun reportError(message: String) {
        val token = errorToken
        successToken = null
        errorToken = null
        if (token != null) dispatchCallback(token, listOf(message))
        else dispatchEvent("onerror", listOf(message))
    }

    private fun dispatchEvent(name: String, args: List<Any> = emptyList()) {
        val payload = JSONObject().put("kind", "event").put("name", name).put("args", JSONArray(args))
        dispatch(payload.toString())
    }

    private fun dispatchCallback(token: String, args: List<Any>) {
        val payload = JSONObject().put("kind", "callback").put("token", token).put("args", JSONArray(args))
        dispatch(payload.toString())
    }

    private fun onMain(block: () -> Unit) {
        if (Looper.myLooper() == Looper.getMainLooper()) block() else main.post(block)
    }

    private fun withPlayer(block: (ExoPlayer) -> Unit) {
        onMain { player?.let(block) }
    }
}
