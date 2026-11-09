# Answer file adapter

`acceptCursorAnswerFile` in `internal/agentd/cursor_answer_file_linux.go` opens one helper answer artifact and passes it to `acceptCursorBoundAnswer`. It is unexported, Linux-only, and has no production caller. The helper, scaffold, binding, decoder, and result join stay unchanged. This file does not turn the opt-in on and it does not launch anything.

The leaf name is not a caller argument. It is `a` plus the hex SHA-256 of the domain `cursor-answer-file-v1`, the native invocation ID, and the launch digest, separated by NUL bytes. That name matches the helper's label rule and is at most 128 characters. The adapter reads only that basename in the task's expected worktree.

## Open

The join checks the run error, the helper outcome, and the current binding before the reader is used. The filesystem walk starts only on that read. The walk opens `/` and then each absolute component with `O_DIRECTORY`, `O_NOFOLLOW`, and `O_CLOEXEC`. Each intermediate descriptor is pinned during that step, and its device and inode are retained. The descriptors are not all held open together. The retained chain covers every directory from `/` through the worktree, at most 128 directories. After the read it walks that path again and compares the whole chain. A new ancestor with the same final worktree inode is a change. The leaf is opened with `O_RDONLY`, `O_NOFOLLOW`, `O_NONBLOCK`, and `O_CLOEXEC`.

The final directory must be owned by the current UID and must not be writable by group or others. The adapter does not change its mode. The leaf must be a regular file, one link, mode `0600` with no special bits, and owned by the current UID. Symlinks, directories, FIFOs, devices, sockets, hard links, and a size above the decoder's encoded bound are refused. The opened descriptor is compared with the directory entry before and after the read. The directory is opened again by the same walk and its identity is compared. A replaced leaf, a replaced ancestor, truncation, growth, or a failed read, validation, or close returns a zero result. Descriptors are closed. The artifact is not unlinked or repaired.

Content that passes those checks is decoded by the existing answer decoder. Decoder failures keep the decoder's fixed class. File failures use a fixed class: `answer-file-path`, `answer-file-absent`, `answer-file-symlink`, `answer-file-type`, `answer-file-changed`, `answer-file-read`, or the decoder's `oversized` when the file metadata is already over the encoded bound. The class does not include the path, the file bytes, an operating-system error, an ID, or a canary. `servedModel` stays `Unknown`.

## Limits

Pinned descriptors and the metadata checks constrain the object that was read. They do not show who wrote it, close every same-UID race, create an atomic task or filesystem lease, interrupt a stalled filesystem call, or prove a provider or model. A trusted run result, a trusted current context, quiescent acquisition, a future command opt-in, and cleanup ownership stay outside this file. Two binding observations remain not a lease. This is not a native or live readiness claim.
