package com.torrenttv.app

import android.webkit.JavascriptInterface
import com.torrenttv.app.avplay.AvPlayBridge

class Bridge(
    private val activity: MainActivity,
    private val network: NetworkInfoSource,
    private val avplay: AvPlayBridge,
) {
    @JavascriptInterface
    fun exit() {
        activity.runOnUiThread { activity.finish() }
    }

    // Empty strings mean "unknown" — Setup falls back to the manual address
    // field, exactly like the Tizen path when webapis.network is missing.
    @JavascriptInterface
    fun getIp(): String = network.ip() ?: ""

    @JavascriptInterface
    fun getSubnetMask(): String = network.subnetMask() ?: ""

    // AVPlay-shaped player commands; see avplay/AvPlayBridge.kt.
    @JavascriptInterface fun open(url: String) = avplay.open(url)
    @JavascriptInterface fun setDisplayRect(x: Int, y: Int, width: Int, height: Int) = avplay.setDisplayRect(x, y, width, height)
    @JavascriptInterface fun setDisplayMethod(mode: String) = avplay.setDisplayMethod(mode)
    @JavascriptInterface fun prepareAsync(successToken: String, errorToken: String) = avplay.prepareAsync(successToken, errorToken)
    @JavascriptInterface fun play() = avplay.play()
    @JavascriptInterface fun pause() = avplay.pause()
    @JavascriptInterface fun seekTo(milliseconds: Double) = avplay.seekTo(milliseconds)
    @JavascriptInterface fun stop() = avplay.stop()
    @JavascriptInterface fun close() = avplay.close()
    @JavascriptInterface fun getDuration(): Double = avplay.getDuration()
    @JavascriptInterface fun getTotalTrackInfo(): String = avplay.getTotalTrackInfo()
    @JavascriptInterface fun setSelectTrack(type: String, index: Int) = avplay.setSelectTrack(type, index)
    @JavascriptInterface fun setSilentSubtitle(silent: Boolean) = avplay.setSilentSubtitle(silent)
}
