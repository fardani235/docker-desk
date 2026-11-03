## Purpose

Defines the rules that make the desk trustworthy as a control plane: nothing is changed or
deleted without a deliberate confirmation of the exact object and effect, nothing in use is
forced, and a prune destroys exactly what it showed.

## ADDED Requirements

### Requirement: No single keystroke mutates
No single key press SHALL cause any request that changes or deletes anything on the daemon.
Every mutating action SHALL first open a confirmation that is completed by a separate,
deliberate input.

#### Scenario: Action key alone
- **WHEN** the operator presses the key for any mutating action
- **THEN** a confirmation is shown and no mutating request has been sent

#### Scenario: Declining
- **WHEN** the operator presses Escape or `n` in a confirmation
- **THEN** the confirmation closes, nothing is sent to the daemon and nothing is recorded as a job

#### Scenario: Enter is not consent
- **WHEN** a confirmation for a state change is shown and the operator presses Enter or any key other than `y`
- **THEN** nothing is sent to the daemon

### Requirement: Only input given to a visible, settled confirmation counts
Input that proceeds with a mutation SHALL be accepted only by a confirmation that is on screen
and has been on screen long enough to be read. Keys typed before a confirmation appeared, or
while it is not drawn, SHALL NOT consent to it.

#### Scenario: Type-ahead
- **WHEN** the operator presses an action key and its confirming key in quick succession, before the confirmation is displayed
- **THEN** nothing is sent to the daemon

#### Scenario: Keys meant for something else
- **WHEN** the operator presses an action key and, before its confirmation appears, starts doing something else (typing a filter, opening another overlay, moving the highlight)
- **THEN** the pending action is dropped, no confirmation appears under those keys, and nothing is sent to the daemon

#### Scenario: Confirmation only just displayed
- **WHEN** a confirming key arrives within the first quarter of a second after the confirmation was displayed
- **THEN** it is ignored, the confirmation stays open, and a later confirming key is accepted

#### Scenario: Confirmation not drawn
- **WHEN** a confirmation is open but the terminal is too small for the desk to be drawn
- **THEN** no key confirms it, and it is shown again, still unconfirmed, when the terminal is large enough

#### Scenario: Preview being recomputed
- **WHEN** the operator switches the scope of a prune preview and types the confirmation before the new list is displayed
- **THEN** nothing is sent, and the new preview is displayed with an empty confirmation input

#### Scenario: Cancelled form
- **WHEN** the operator cancels a form while its validation is still in progress
- **THEN** the form is not submitted when the validation completes

### Requirement: Confirmations name the exact object, effect and daemon
A confirmation SHALL state the action, the kind of object, the object's name and short ID, what
the effect will be in plain words, and the daemon endpoint it will be sent to.

#### Scenario: Stopping a container
- **WHEN** the operator asks to stop container `web`
- **THEN** the confirmation names `web`, its short ID, says the container will be stopped (signalled, then killed after the grace period), and shows the daemon endpoint

#### Scenario: Removing an image tag that is not the last
- **WHEN** the operator asks to remove an image reference and the image has other tags
- **THEN** the confirmation says only the reference will be untagged and the image data will remain

### Requirement: Confirmations are bound to the object they showed
A confirmation SHALL act on the object that was highlighted when it was opened, identified by
its daemon identifier, regardless of any change to the list or highlight while it is open.

#### Scenario: List reorders under an open confirmation
- **WHEN** a confirmation for container A is open and a refresh moves another container B under the highlight
- **THEN** confirming acts on container A

#### Scenario: Action chosen from the action menu
- **WHEN** the action menu was opened for object A and the highlight has moved to object B before an action is chosen
- **THEN** the action applies to object A, or reports that A no longer exists; it never applies to B

#### Scenario: A name now means a different object
- **WHEN** an image reference or volume name that was confirmed for removal has, by the time the request is about to be sent, been moved to a different image or reused by a different volume
- **THEN** nothing is removed and the job reports that the name now refers to something else

#### Scenario: An untag that would now delete
- **WHEN** removing a reference was confirmed as an untag that leaves the image in place, and that reference has since become the image's last one
- **THEN** nothing is removed and the job says why

### Requirement: Deletions require a typed confirmation
Actions that delete — removing a container, image, volume or network, removing a compose
project's containers, and any prune — SHALL require the operator to type a confirmation word
shown in the dialog and press Enter. State changes that can be reversed — start, stop, restart,
pause, unpause, kill, network connect and disconnect, stopping a compose project — SHALL
require pressing `y`.

#### Scenario: Typed word matches
- **WHEN** the operator types the shown word exactly and presses Enter
- **THEN** the deletion is started as a job

#### Scenario: Typed word does not match
- **WHEN** the operator presses Enter with anything else typed
- **THEN** nothing is sent, the confirmation stays open and says the text does not match

### Requirement: In-use objects are refused, never forced
The program SHALL NOT send a forced removal. When the object to delete is in use, the
confirmation SHALL say what uses it and SHALL offer no way to proceed. If the daemon itself
reports that an object is in use, that message SHALL be shown.

#### Scenario: Removing a running container
- **WHEN** the operator asks to remove a container that is running, paused or restarting
- **THEN** the dialog states that the container is running and must be stopped first, and no removal can be confirmed

#### Scenario: Removing an image used by a container
- **WHEN** the operator asks to remove an image that any container, running or stopped, was created from
- **THEN** the dialog lists those containers and no removal can be confirmed

#### Scenario: Removing a volume used by a container
- **WHEN** the operator asks to remove a volume referenced by any container
- **THEN** the dialog lists those containers and no removal can be confirmed

#### Scenario: Removing a network with attached containers
- **WHEN** the operator asks to remove a network that containers are attached to or configured to use
- **THEN** the dialog lists those containers and no removal can be confirmed

#### Scenario: Removing a predefined network
- **WHEN** the operator asks to remove the `bridge`, `host` or `none` network
- **THEN** the dialog states that predefined networks cannot be removed

#### Scenario: Object becomes in use after confirmation
- **WHEN** a deletion was confirmed and the daemon answers that the object is in use
- **THEN** the object is left alone and the daemon's message is shown as the job's error

### Requirement: A prune shows exactly what it will destroy and destroys only that
Before a prune is confirmed the dialog SHALL list every individual object it will delete, by
name and short ID, with the total count (and total size for images). After confirmation the
program SHALL delete exactly the listed objects — each one individually, without force — and
nothing that was not listed, even if more objects became eligible in the meantime.

#### Scenario: Preview
- **WHEN** the operator asks to prune images
- **THEN** the dialog lists each image that would be removed and the total size, and the list can be scrolled if it does not fit

#### Scenario: Nothing to prune
- **WHEN** nothing is eligible
- **THEN** the dialog says there is nothing to prune and offers nothing to confirm

#### Scenario: New eligible object appears after the preview
- **WHEN** another unused volume is created after the preview was shown and the operator then confirms
- **THEN** only the volumes that were listed are deleted and the new one is left alone

#### Scenario: Listed object became in use
- **WHEN** a listed object is in use by the time its deletion is attempted
- **THEN** it is not deleted, the daemon's refusal is recorded for it, and the remaining listed objects are still processed

#### Scenario: Listed tagged image became in use
- **WHEN** a tagged image listed by an "all unused" prune is used by a container by the time its references are about to be removed
- **THEN** none of its references is removed, although the daemon itself would allow an untag

#### Scenario: Prune eligibility is conservative
- **WHEN** a network or volume is referenced only by stopped containers
- **THEN** it is treated as in use and is not offered for pruning
