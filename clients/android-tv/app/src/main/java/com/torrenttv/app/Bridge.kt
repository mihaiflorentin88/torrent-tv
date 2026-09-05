package com.torrenttv.app

import android.webkit.JavascriptInterface

class Bridge(private val activity: MainActivity, private val network: NetworkInfoSource) {
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
}
