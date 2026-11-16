- Never implement anything that violates requirements.md

- Never implement code if I don't ask you directly

- Keep the number of files small because this codebase is 
  critical and must be correct!

- Every GO file must have a corresponding test file

- Never peek into the implementation of the Juice kernel

- For every user-story/flow you want to implement, you must try
  it out yourself on a test kernel first (in /tmp). Simulate the
  surface and double check it works! Then clean the files.

- These belong to the official kernel documentation:
  - docs/API.md
  - docs/flows.sh
  - docs/juice-requirements.md
  These are files I write manually, and you must never change them,
  only commit them.

- When you plan an implementation, always add "pass all unit tests
  AND flows/user-stories". Never attempt to commit before passing
  all tests and flows!

- Follow the visual UI grammar. Never implement inline styling,
  always focus on reusable components.

- Aggressively unify code paths: one concept has one component, one
  route and one store. A second rendering, route, copy of a rule or
  local mirror of kernel data is a defect; unify before adding. No
  overengineering.

- A request waits only for the kernel it is about, and for any other at
  most one second. What a page shows from another source is what the
  session last read of it; each source is read on its own, writes only
  its own entry, and has its own back-off.

- Never add a Claude-Session trailer or any session identifier to commit messages.
  

Answering questions
===================

In all your answers:

- use academic prose and tone
- be concise and direct
- never beat around the bush, never use theatricals
- do not introduce vague nomenclature, stick to what's being used
  or to what's standard in the literature
- always provide logical or mathematical justification.

