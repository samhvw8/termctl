"""Exceptions with process exit codes. A code is part of the CLI contract."""


class TermctlError(Exception):
    code = 1


class BackendError(TermctlError):
    """The terminal backend failed, or does not support this."""

    code = 1


class UsageError(TermctlError):
    code = 2


class TargetError(TermctlError):
    """Target resolved to zero, several, or the caller's own pane."""

    code = 2


class DraftError(TermctlError):
    """The target's input line holds text, or it cannot be read."""

    code = 4


class WaitTimeout(TermctlError):
    code = 5
