/** Kepler stub: memory file detection (memdir disabled). */
export function isMemoryFile(_path: string): boolean { return false; }
export function isAutoMemFile(_path: string): boolean { return false; }
export function isAutoManagedMemoryFile(_path: string): boolean { return false; }
export function isAutoManagedMemoryPattern(_path: string): boolean { return false; }
export function isMemoryDirectory(_path: string): boolean { return false; }
export function isShellCommandTargetingMemory(_cmd: string): boolean { return false; }
