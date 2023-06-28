# EmbedMeUp

Create and query embeddings on the CLI using the [Pinecone](https://pinecone.io) vector store. Use it with [ThoughtLoom](https://github.com/tbiehn/thoughtloom) to perform retrieval augmented generation. Or, just use it as a 'local' search.
We rely on `text-embedding-ada-002` for embeddings.

## Features

- Incorporate the embeddings retrieval into CLI workflows.
- JSON in, JSON out

## Installation

```bash
go install github.com/tbiehn/embedmeup@latest
```

## Usage

1. Set your OpenAI API key using the 'OPENAI_API_KEY' environment variable or Azure AI API key using the 'AZUREAI_API_KEY' environment variable.
2. Set your Pinecone API key using the 'PINECONE_API_KEY' environment variable.

You'll need a pinecone (free / starter) account, with an index and project name.
Make an index with `1536` dimensions. You'll see a URL in the dashboard; `[Index]-[Project].svc.[Region].pinecone.io`. Invoke `embedmeup` as `embedmeup -index=[Index] -region=[Region] -project [Project]`.

Consider using a namespace that can be used to manage various collections of embeddings in one index by using the `-namespace` flag for either upsert, retrieve, or deleteAll operations.

_WARNING: Embedding API requests incur costs. I haven't implemented dry run support yet. But they're relatively inexpensive._

### Upsert

Insert the embeddings for chunks of input using `-mode upsert`. The tool takes individual JSON objects, then calculates the embedding over a single string object contained in them, the ID for the embedding is the SHA256 hash of the input JSON object, and this object is stored on the filesystem (`~/.embedmeup/embeddings` or specify with `-edir`).

Supply input JSON data through stdin - by default `embedmeup` looks for a string field `search` to calculate the embedding over, which can be specified with `-param`.

In this example we take all the markdown files in a directory tree, then use jq to construct json objects of the form `{"filename":"...", "contents": "..."}`. Then we instruct `embedmeup` to calculate an embedding over the `contents` field, and perform bisection chunking to keep each chunk under `512` tokens.

```bash
for file in **/*.md; do jq -n --arg fn "$file" --rawfile fc "$file" '{filename: $fn, contents: $fc}'; done | embedmeup -index=[INDEX] -region=[REGION] -project [PROJECT] -mode upsert -namespace mdreports -tokens 512 -param contents
```

The tool includes a default recusive bisection based first on lines, then on words, and finally on characters. It's in there for rapid prototyping and convenience, there's better strategies you can use before passing a chunk for upsert.

### Retrieve

Perform k-nearest-neighbors retrieval for an input using `-mode retrieve`. Specify the number of nearest neighbors with `-topK`, and the parameter to calculate the embedding over with `param`. Input JSON objects will be passed through nested under `.Input` and retrievals will be nested under `.Response`.

In this example, I've ingested a number of Trail of Bits public security reports into the 'mdreports' namespace, I'll search for buffer overflow related findings, and injection related findings;

```bash
echo '{"search":"A buffer overflow in the function."}{"search":"Cross site scripting"}' | embedmeup -index=[INDEX] -region=[REGION] -project [PROJECT] -mode retrieve -topK 2 -namespace mdreports -param search | jq
```

Output:
```json
{
  "Input": {
    "search": "A buffer overflow in the function."
  },
  "Response": [
    {
      "contents": "Data Validation   High\nsmaller than user balances\n© 2020 Trail of Bits\nOrigin Dollar Assessment | 17\n/\n1. Invalid vaultBu\u0000fer could revert allocate\nSeverity: Low\nType: Data Validation\nTarget: VaultAdmin.sol, VaultCore.sol\nDiﬃculty: High\nFinding ID: TOB-OUSD-001\nDescription\nThe lack of input validation when updating the vaultBuffer could cause token allocations\ninside allocate to revert when no revert is expected.\nfunction setVaultBuffer ( uint256 \\_vaultBuffer ) external onlyGovernor {\nvaultBuffer = \\_vaultBuffer;\n}\nFigure 1.1: VaultAdmin.sol#L50-L52\nEvery account can call allocate to allocate excess tokens in the Vault to the strategies to\nearn interest.\nThe vaultBuffer indicates how much percent of the tokens inside the Vault to allocate to\nstrategies (to earn interest) when allocate is called. The setVaultBuffer function allows\nvaultBuffer to be set to a value above 1e18(=100%). This function can only be called by\nthe Governor contract, which is a multi-sig. Mistakenly proposing 1e19(=1000%) instead of\n1e18 might not be noticed by the Governor participants.\nIf the vaultBuffer is above 1e18 and at least one of the strategies has been allocated\nsome tokens, the function will simply return. However, in case none of the strategies have\nyet been allocated any tokens, the vaultBuffer is subtracted from 1e18 causing an\nunderﬂow. Depending on the result of the underﬂow, this could cause a revert when the\nVault contract tries to transfer tokens to a strategy since the contract does not possess\nthat amount of tokens. What would be expected in this situation is for no allocations to\noccur and the transaction to successfully execute, instead of reverting.\nThis issue could be mitigated by preventing the underﬂow by e.g. using SafeMath.\nHowever, the root cause is the lack of input validation in VaultAdmin . Such is the case for\nmost of the other functions inside VaultAdmin .\nThis issue serves as an example as there is no input validation in any function protected by\nthe onlyGovernor modiﬁer.\nExploit Scenario\nNo strategies have been allocated any tokens yet. Bob intends to create a proposal to",
      "filename": "OriginDollar.pdf.md"
    },
    {
      "contents": "#9 0x55d812f7609e in operate /home/scooby/curl/src/tool\\_operate. c : 2732\n#10 0x55d812f4ffa8 in main /home/scooby/curl/src/tool\\_main. c : 276\n#11 0x7f9b5f1aa082 in \\_\\_libc\\_start\\_main ../csu/libc- start . c : 308\n#12 0x55d812f506cd in \\_start (/usr/ local /bin/curl+ 0x316cd )\n0x611000004780 is located 0 bytes inside of 256-byte region [0x611000004780,0x611000004880)\nfreed by thread T0 here:\n#0 0x7f9b5f9b140f in \\_\\_interceptor\\_free\n../../../../src/libsanitizer/asan/asan\\_malloc\\_linux.cc:122\n#1 0x55d812f75682 in add\\_parallel\\_transfers /home/scooby/curl/src/tool\\_operate.c:2251\npreviously allocated by thread T0 here:\n#0 0x7f9b5f9b1808 in \\_\\_interceptor\\_malloc\n../../../../src/libsanitizer/asan/asan\\_malloc\\_linux.cc:144\n#1 0x55d812f75589 in add\\_parallel\\_transfers /home/scooby/curl/src/tool\\_operate.c:2228\nSUMMARY: AddressSanitizer: heap-use-after-free\n../../../../src/libsanitizer/asan/asan\\_interceptors.cc:431 in \\_\\_interceptor\\_strcpy\nShadow bytes around the buggy address:\n0x0c227fff88a0: fd fd fd fd fd fd fd fd fd fd fd fd fd fd fd fd\n0x0c227fff88b0: fd fd fd fd fd fd fd fd fd fd fd fd fd fd fd fd\n0x0c227fff88c0: fa fa fa fa fa fa fa fa fd fd fd fd fd fd fd fd\n0x0c227fff88d0: fd fd fd fd fd fd fd fd fd fd fd fd fd fd fd fd\n0x0c227fff88e0: fd fd fd fd fd fd fd fd fa fa fa fa fa fa fa fa",
      "filename": "2022-12-curl-securityreview.pdf.md"
    }
  ]
}
{
  "Input": {
    "search": "Cross site scripting"
  },
  "Response": [
    {
      "contents": "5. Tab injection in cookie ﬁle\n6. Standard output/input/error may not be opened\n7. Double free when using HTTP proxy with speciﬁc protocols\n8. Some ﬂags override previous instances of themselves\n9. Cookies are not stripped after redirect\n10. Use after free while using parallel option and sequences\n11. Unused memory blocks are not freed resulting in memory leaks\n12. Referer header is generated in insecure manner\n23\n25\n27\n29\n31\n32\n35\n36\n37\n40\n42\n13. Redirect to localhost and local network is possible (Server-side request forgery\nlike)\n43\n14. URL parsing from redirect is incorrect when no path separator is provided\n44\nSummary of Recommendations\nA. Vulnerability Categories\nB. Code Maturity Categories\nC. Code Quality Recommendations\nD. HSTS debug patch\nE. Fix Review Results\nDetailed Fix Review Results\n47\n48\n50\n52\n53\n54\n56\nTrail of Bits\n4\ncURL Security Assessment\nPUBLIC\nExecutive Summary\nEngagement Overview\nThe Linux Foundation, via OpenSSF and strategic partner Open Source Technology\nImprovement Fund, engaged Trail of Bits to review the security of cURL. From September\n12 to October 7, 2022, a team of four Trail of Bits consultants conducted a security review\nof the client-provided source code, with ﬁve and a half engineer-weeks of eﬀort. Since this\nproject coincided with a Trail of Bits Maker Week, six additional people contributed ﬁve\nadditional days of eﬀort. Details of the project’s timeline, test targets, and coverage are\nprovided in subsequent sections of this report.\nProject Scope\nOur testing eﬀorts were focused on the identiﬁcation of ﬂaws that could result in a\ncompromise of conﬁdentiality, integrity, or availability of the target system. We conducted\nthis audit with full knowledge of the system. We had access to the cURL source code,\ndocumentation, and fuzzing harnesses. We performed static and dynamic automated and\nmanual testing of the target system and its codebase, using both automated and manual\nprocesses.\nSummary of Findings\nThe audit uncovered a small number of signiﬁcant ﬂaws that could impact system",
      "filename": "2022-12-curl-securityreview.pdf.md"
    },
    {
      "contents": "iOS signiﬁcantly more difﬁcult than on desktop operating systems and Mandatory Code Signing similarly makes installing\nunauthorized software on iOS-based devices signiﬁcantly more difﬁcult than doing so on a desktop operating system.\nWhile Google’s Android and RIM’s BlackBerry OS mobile operating systems implement similar features to the Mandatory\nCode Signing found in iOS, neither of them implement any non-executable data memory protections. Again, this makes\nremote injection and execution of native code easier on these platforms than on Apple’s iOS. In addition, it should be\nnoted that all three platforms include mobile web browsers based on the same open-source WebKit HTML rendering\nengine. This means that all three platforms will likely be affected by any vulnerabilities identiﬁed in this component and, in\nfact, many such vulnerabilities have been identiﬁed over the last several years 8.\nWith iOS 4.3 and presumably later versions, the dynamic-codesigning entitlement in MobileSafari that is required to\npermit native code JavaScript JIT compilation also allows remote browser-based exploits to inject and execute native\ncode. On previous versions of iOS and within applications that do not posses this entitlement, an attacker may only\nrepurpose already-loaded native code in their attack. While this has been shown to be Turing-complete and therefore\nequivalent to arbitrary native code execution9, it is signiﬁcantly more work and not as reusable across target versions or\napplications as native code. In addition, the introduction of Address Space Layout Randomization in iOS 4.3 signiﬁcantly\ncomplicates code-reuse attacks as well as any taking advantage of Dynamic Code Signing by requiring the attacker to\nalso discover and exploit a memory disclosure vulnerability.\n8 http://osvdb.org/search?search%5Bvuln\\_title%5D=WebKit\n9 Shacham, Hovav. “The Geometry of Innocent Flesh on the Bone”, Proceedings of CCS 2007, ACM Press.\nApple iOS Security Evaluation\n14\nTrail of Bits\nSandboxing\nIntroduction\nThe iOS application-based security model requires that applications and their data are isolated from other applications.\nThe iOS Sandbox is designed to enforce this application separation as well as protect the underlying operating system\nfrom modiﬁcation by a potentially malicious application. It does so by assigning each installed application a private area of",
      "filename": "Trail_of_Bits_-_Apple_iOS_4.pdf.md"
    }
  ]
}
```

You can define [ThoughtLoom](https://github.com/tbiehn/thoughtloom) templates that make use of the context and the inputs and perform retrieval augmented generation by piping `embedmeup` to `thoughtloom`.

### Empty out a vector store

Use `-mode deleteAll` to empty out a specified `-namespace`. We don't clear the related files from the local embedding directory.

```bash
embedmeup -index=[INDEX] -region=[REGION] -project [PROJECT] -mode deleteAll -namespace mdreports
```

## License

EmbedMeUp is released under Apache 2.0.