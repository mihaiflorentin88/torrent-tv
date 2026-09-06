package com.torrenttv.app

/**
 * Synthetic key events that forward the Android DPAD and ENTER keys into the
 * page as the arrow and Enter DOM events the web app's focus engine consumes
 * — the same contract Tizen delivers through tvinputdevice. Consuming the
 * native keys also takes the WebView's own focus search out of the loop, so
 * one remote press moves focus exactly once.
 */
object DpadKeys {
    fun downScript(keyCode: Int): String? = keyScript("keydown", keyCode)
    fun upScript(keyCode: Int): String? = keyScript("keyup", keyCode)

    private fun keyScript(type: String, keyCode: Int): String? {
        val (name, code) = when (keyCode) {
            KeyEvent.KEYCODE_DPAD_LEFT -> "ArrowLeft" to 37
            KeyEvent.KEYCODE_DPAD_RIGHT -> "ArrowRight" to 39
            KeyEvent.KEYCODE_DPAD_UP -> "ArrowUp" to 38
            KeyEvent.KEYCODE_DPAD_DOWN -> "ArrowDown" to 40
            KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER -> "Enter" to 13
            else -> return null
        }
        // Old TV WebViews ignore parts of the KeyboardEvent init dict, so
        // key/keyCode/which are forced with defineProperty after init: the
        // page's remoteAction matches on either field.
        return "(function(){" +
            "var e=document.createEvent('KeyboardEvent');" +
            "e.initKeyboardEvent('$type',true,true,window,'$name',0,'',false,'');" +
            "Object.defineProperty(e,'key',{get:function(){return '$name'}});" +
            "Object.defineProperty(e,'keyCode',{get:function(){return $code}});" +
            "Object.defineProperty(e,'which',{get:function(){return $code}});" +
            "document.dispatchEvent(e);})();"
    }

    private object KeyEvent {
        const val KEYCODE_DPAD_LEFT = 21
        const val KEYCODE_DPAD_RIGHT = 22
        const val KEYCODE_DPAD_UP = 19
        const val KEYCODE_DPAD_DOWN = 20
        const val KEYCODE_DPAD_CENTER = 23
        const val KEYCODE_ENTER = 66
    }
}
