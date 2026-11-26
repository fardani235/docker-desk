# Spec Delta

## MODIFIED Requirements

### Requirement: Unfocused daemons keep an event stream and a ping
A connected daemon that is not focused SHALL keep one event stream and a periodic ping. It
SHALL NOT keep statistics, log or list refreshes running for itself while it is unfocused.
While an explicitly opened view such as Attention is on screen, that view MAY read the
member's container list and its containers' details; those reads SHALL stop when the view
closes and SHALL NOT become a periodic background refresh.

#### Scenario: Background daemon goes quiet
- **WHEN** the connection to an unfocused daemon is lost
- **THEN** the status line says so with the daemon's name, and the entry shows it disconnected

#### Scenario: Focusing a background daemon
- **WHEN** the operator focuses a connected daemon that was not focused
- **THEN** its lists are requested at once, the panes show a loading state until they answer, and the previous daemon's data is never shown as current

#### Scenario: Attention reads a background daemon
- **WHEN** the operator opens Attention while another daemon is connected and unfocused
- **THEN** that daemon's containers are read for the view, and no periodic list refresh starts

#### Scenario: Attention closes
- **WHEN** the Attention view closes
- **THEN** no further read is made for it, and the daemon keeps only its event stream and ping
