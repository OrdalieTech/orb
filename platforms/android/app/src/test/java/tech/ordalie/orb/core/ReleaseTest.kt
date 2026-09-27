package tech.ordalie.orb.core

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ReleaseTest {
    @Test fun onlyALaterReleaseIsNewer() {
        assertTrue(Release.newer("0.13.0", "0.12.99"))
        assertTrue(Release.newer("0.13.1", "0.13.0"))
        assertTrue(Release.newer("1.0.0", "0.99.9"))
        assertTrue(Release.newer("0.13.0", "0.0.0-dev"))
        assertFalse(Release.newer("0.13.0", "0.13.0"))
        assertFalse(Release.newer("0.12.1", "0.13.0"))
        assertFalse(Release.newer("0.13", "0.13.0"))
    }
}
