package com.torrenttv.app.avplay

import androidx.media3.common.Player

/** ExoPlayer playback state to the AVPlay listener callback name. */
object AvPlayStateMapper {
    fun bufferingCallback(state: Int): String? = when (state) {
        Player.STATE_BUFFERING -> "onbufferingstart"
        Player.STATE_READY -> "onbufferingcomplete"
        Player.STATE_ENDED -> "onstreamcompleted"
        else -> null
    }
}
