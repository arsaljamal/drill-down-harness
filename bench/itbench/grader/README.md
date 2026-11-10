# ITBench-AA grader

`grade.py` and `matching.py` are copied unchanged from the Harbor ITBench-AA adapter
(harbor-framework/adapters, pull request #14, `DanielBlei/adapters b6da30575e2cf14c2d90436f154290ac297cd854`), licensed Apache-2.0.
They implement Artificial Analysis' published scoring: average precision at full recall
over root-cause entities, with the same matching rules. Using the upstream grader keeps
our scores comparable with other ITBench-AA results.

Dataset: ArtificialAnalysis/ITBench-AA on Hugging Face (CC-BY-4.0), from IBM's ITBench.
Please cite the ITBench paper when using these results.
