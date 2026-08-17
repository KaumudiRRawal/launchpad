import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// Unmount anything a test rendered. Without this a component left mounted by
// one test keeps polling into the next one, which turns a real failure into a
// confusing one somewhere else.
afterEach(cleanup)
