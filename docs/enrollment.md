# Enrolling bays from the page

**Status: proposal (xram, 2026-11-07).  Nothing here is built.**  Today a bay needs `deploy/ca.sh`, the root's password at a terminal, and a key copied by hand (bringup.md §3).  That's fine for one person with a few docks.  It doesn't scale to a bay in every lab, run by people who will never touch the CA machine.

This replaces the hand-minting with a connection manager on the hub: an admin adds a host on the page, and the host enrolls itself.

## The shape

- **The root stays offline**, as spec §8 has it.  Its one new job is to sign an **issuing intermediate**: client certificates only (EKU `clientAuth`), `pathlen:0`, nothing else.
- **The hub holds the intermediate, encrypted.**  An admin unlocks it with a passphrase.  The key lives only in memory: no core dumps, the page locked in RAM, and it locks again after a timeout.  A hub restart, including every deploy, locks it, and the wall says **CA locked** so a failed enrollment explains itself.  Bays already enrolled don't notice, since verifying a bay needs only the root.
- **The dock host makes its own key.**  The admin adds a host on the page and gets a one-time token.  On the host, the link side (the `zawiper` user, network and no disks, never `bayd`) generates a key, and sends a CSR with the token.  The hub signs it with the intermediate.  Bay keys never leave the host they belong to, so there's no key folder to clean up afterwards.

**The case this is built for** is a hub broken into while the CA is unlocked.  The attacker can mint fake *bays*, which the revocation list handles.  They can't mint a fake *hub*.  Unlocking the root from the page would hand them that: one certificate, and every bay trusts an impostor.

## Decided

- **Keycloak decides; Globus only vouches** (xram, 2026-11-07).  The hub speaks OIDC to one place, Keycloak.  Keycloak brokers Globus as an identity provider.  Globus says who someone is, and Keycloak groups, which we run, say what they may do: add hosts, unlock the CA, enroll.  The groups ride in the token through a mapper, so the hub needs no Groups API and no Globus-specific code, and any other IdP Keycloak can broker works the same way.
- **A first login gets nothing.**  The first-broker-login flow creates the Keycloak user with no groups.  An admin adds them.  Logging in through Globus never grants a right on its own.
- **Break-glass:** a local Keycloak admin, on the same login path, for when Globus is down.  If Keycloak itself is down, bays keep wiping and the wall stays readable, and the hub host's own command line (decision 4 below) can still unlock and enroll.
- **Audit on the hub:** who added each host, who unlocked the CA and when, and what each token enrolled.  Group membership is ours now, so Keycloak's admin events record who granted what.

## To decide before building

1. **The token carries the root's fingerprint.**  A fresh host has no `ca.crt`, so it checks the hub against the fingerprint before sending anything (like `kubeadm join --discovery-token-ca-cert-hash`).  Tokens are single-use and short-lived (minutes).  Each is bound to the host name the admin typed, and the hub refuses a CSR named anything else.  The CN stays `bay/<host>/<name>`.
2. **The hub pins its intermediates.**  Trusting the root alone would keep a leaked intermediate's certificates valid until they expire.  With a pin, rotating one is a config change.  The revocation list (§10, Next) is a fingerprint denylist next to that pin.
3. **A test, not a belief:** a server certificate chained through the intermediate must be refused by a bay.  Go checks the EKU across the whole chain, so it should be.  The test proves it, and keeps it true.
4. **Where the passphrase travels.**  Through the reverse proxy, it crosses the hub host in plain text after TLS ends.  Either take the unlock on the direct-TLS port the links use, or make it a command on the hub host.
5. **Host names are the hub's to give.**  Two departments will each have a `lab-a`.  The hub refuses a duplicate, or assigns the name itself.
6. **Stateless hosts.**  A live USB image loses its key at every boot.  Either it keeps a persistence partition, or it enrolls at boot with a long-lived token, and that token then needs the same care as a key.

## Testing

A test Keycloak realm with a mock identity provider in front of it runs the whole flow without a real Globus registration: login, the empty first login, group checks, unlock, enroll, refuse.
