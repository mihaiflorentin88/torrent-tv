package com.torrenttv.app

import android.webkit.JavascriptInterface

class Bridge(private val activity: MainActivity) {
    @JavascriptInterface
    fun exit() {
        activity.runOnUiThread { activity.finish() }
    }
}
