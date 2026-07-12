# Project Information

The project is called *NextLeaf*, and is a self-hosted book-recommendation service focused on *variation* not strict relevance. Based on TBR lists, it should give recommendations that make you more varied in your reading, making sure that you do not keep to one genre or author.

## Development style and conventions

- Try to use a Test-Driven Development approach. Develop the test-first. Write failing test before the implementation. Keep tests beside the code that they cover. Make the tests meaningful. Do not write junk-tests just to have them. Cover edge-cases and make sure to cover both invalid uses and the happy-path.
- Keep to the standard library. Any deviation should be mediated/asked about with the operator. If a dependency could is thought about/evaluated, bring it up *immediately* with the operator so they can take a stance whether it be incorporated or not.
- Try to keep build outputs to the `./build` directory.
- Avoid overly commenting code. Keep the comments short, succint and specific. Only document behaviour where the code is not obvious, or where you need information that may only be found elsewhere to fully understand.
- Keep to GO idioms and precidence when writing, to make it as conventional as possible.
- Make sure things work by running the server and serving the webapp locally. Prefer using `go run` instead of build for running the development preview. Never just show the HTML, show the whole application to the operator.

## Project Notes

- The main entrypoint is cmd/nextleaf.
- The project is a web-app.