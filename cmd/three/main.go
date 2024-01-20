package main

/**

Thinking about a task list application with terminal and web clients coordinating on a small list of "tickets".

Let's out of scope the web client for now and focus on two services:

1. The CLI / TermUI one
2. A relay kind of server to store the persistent state and provide it over an API for clients

We could just do term-ui to term-ui but we want to do something that could have an arbitrary number of peers coordinating.

We could do a Sync API:

1. Client posts the stored sync cookie (or none) with a list of known messages
2. Server decodes or creates the sync cookie and applies the messages
3. Server sends back any messages it knows about and its encoded cookie
4. Client applies the messages and stores the cookie
5. Repeat until no messages exchanged.

Storing the doc.. this is the interesting question, how do we store the servers version of the document efficiently.

One approach would be:

1. Load the "latest" doc
2. Load all changes for that doc in order
3. apply the doc
4. calculate and store the changes from the received messages
5. if the total number of changes for the doc is > N, store a new latest copy of the doc
6. Kind of have to grab a lock here or some other mechanism to update things via a transaction

---

Hard to tell what the optimal way of storing changes on disk or in a database might be. Feels like there's an IO
tradeoff between total size and the number of incremental changes to load.

*/
