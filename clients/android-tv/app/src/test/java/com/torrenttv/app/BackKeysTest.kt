package com.torrenttv.app

import org.junit.Assert.assertTrue
import org.junit.Test

class BackKeysTest {
    @Test fun `down script dispatches the Tizen back key`() {
        val script = BackKeys.downScript()
        assertTrue(script.contains("keydown"))
        assertTrue(script.contains("XF86Back"))
        assertTrue(script.contains("10009"))
    }
    @Test fun `up script dispatches the matching keyup`() {
        val script = BackKeys.upScript()
        assertTrue(script.contains("keyup"))
        assertTrue(script.contains("10009"))
    }
}
