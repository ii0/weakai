package neuralnet

import (
	"sync"

	"github.com/unixpickle/autofunc"
	"github.com/unixpickle/num-analysis/linalg"
	"github.com/unixpickle/sgd"
)

// A CostFunc is a cost function (aka loss function)
// used to train a neural network.
//
// It may be beneficial for CostFuncs to lazily
// compute their outputs, since they may be used
// solely for their derivatives.
type CostFunc interface {
	Cost(expected linalg.Vector, actual autofunc.Result) autofunc.Result
	CostR(v autofunc.RVector, expected linalg.Vector,
		actual autofunc.RResult) autofunc.RResult
}

// TotalCost returns the total cost of a layer on a
// set of VectorSamples.
// The elements of s must be VectorSamples.
func TotalCost(c CostFunc, layer autofunc.Func, s sgd.SampleSet) float64 {
	var totalCost float64
	for i := 0; i < s.Len(); i++ {
		sample := s.GetSample(i)
		vs := sample.(VectorSample)
		inVar := &autofunc.Variable{vs.Input}
		result := layer.Apply(inVar)
		costOut := c.Cost(vs.Output, result)
		totalCost += costOut.Output()[0]
	}
	return totalCost
}

// TotalCostBatcher is like TotalCost, but it applies a
// batcher to multiple inputs at once.
// If batchSize is 0, the full sample set will be applied
// at once.
func TotalCostBatcher(c CostFunc, b autofunc.Batcher, s sgd.SampleSet, batchSize int) float64 {
	var totalCost float64
	i := 0
	for i < s.Len() {
		bs := batchSize
		if bs == 0 || bs > s.Len()-i {
			bs = s.Len() - i
		}
		var input, desired linalg.Vector
		for j := 0; j < bs; j++ {
			sample := s.GetSample(j + i).(VectorSample)
			input = append(input, sample.Input...)
			desired = append(desired, sample.Output...)
		}
		inVar := &autofunc.Variable{Vector: input}
		result := b.Batch(inVar, bs)
		costOut := c.Cost(desired, result)
		totalCost += costOut.Output()[0]
		i += bs
	}
	return totalCost
}

// MeanSquaredCost computes the cost as ||a-x||^2
// where a is the actual output and x is the desired
// output.
type MeanSquaredCost struct{}

func (_ MeanSquaredCost) Cost(x linalg.Vector, a autofunc.Result) autofunc.Result {
	return &meanSquaredResult{
		Actual:   a,
		Expected: x,
	}
}

func (_ MeanSquaredCost) CostR(v autofunc.RVector, a linalg.Vector,
	x autofunc.RResult) autofunc.RResult {
	aVar := &autofunc.Variable{a.Copy().Scale(-1)}
	aVarR := autofunc.NewRVariable(aVar, v)
	return autofunc.SquaredNorm{}.ApplyR(v, autofunc.AddR(aVarR, x))
}

type meanSquaredResult struct {
	OutputLock   sync.RWMutex
	OutputVector linalg.Vector

	Actual   autofunc.Result
	Expected linalg.Vector
}

func (m *meanSquaredResult) Output() linalg.Vector {
	m.OutputLock.RLock()
	if m.OutputVector != nil {
		m.OutputLock.RUnlock()
		return m.OutputVector
	}
	m.OutputLock.RUnlock()
	m.OutputLock.Lock()
	defer m.OutputLock.Unlock()
	if m.OutputVector != nil {
		return m.OutputVector
	}
	var sum float64
	for i, a := range m.Actual.Output() {
		diff := a - m.Expected[i]
		sum += diff * diff
	}
	m.OutputVector = linalg.Vector{sum}
	return m.OutputVector
}

func (m *meanSquaredResult) Constant(g autofunc.Gradient) bool {
	return m.Actual.Constant(g)
}

func (m *meanSquaredResult) PropagateGradient(upstream linalg.Vector, grad autofunc.Gradient) {
	if !m.Actual.Constant(grad) {
		out := m.Actual.Output()
		upstreamGrad := upstream[0]
		downstream := make(linalg.Vector, len(out))
		for i, a := range out {
			downstream[i] = 2 * upstreamGrad * (a - m.Expected[i])
		}
		m.Actual.PropagateGradient(downstream, grad)
	}
}

// AbsCost implements the L1 cost.
// In other words, it computes the sum of the absolute
// differences between actual and expected values.
type AbsCost struct{}

func (_ AbsCost) Cost(x linalg.Vector, a autofunc.Result) autofunc.Result {
	xVar := &autofunc.Variable{Vector: x.Copy().Scale(-1)}
	diff := autofunc.Add(xVar, a)
	mask := &autofunc.Variable{Vector: make(linalg.Vector, len(x))}
	for i, val := range diff.Output() {
		if val < 0 {
			mask.Vector[i] = -1
		} else {
			mask.Vector[i] = 1
		}
	}
	return autofunc.SumAll(autofunc.Mul(mask, diff))
}

func (_ AbsCost) CostR(v autofunc.RVector, x linalg.Vector, a autofunc.RResult) autofunc.RResult {
	xVar := autofunc.NewRVariable(&autofunc.Variable{Vector: x.Copy().Scale(-1)}, v)
	diff := autofunc.AddR(xVar, a)
	mask := &autofunc.Variable{Vector: make(linalg.Vector, len(x))}
	for i, val := range diff.Output() {
		if val < 0 {
			mask.Vector[i] = -1
		} else {
			mask.Vector[i] = 1
		}
	}
	return autofunc.SumAllR(autofunc.MulR(autofunc.NewRVariable(mask, v), diff))
}

// CrossEntropyCost computes the cost using the
// definition of cross entropy.
type CrossEntropyCost struct{}

func (_ CrossEntropyCost) Cost(x linalg.Vector, a autofunc.Result) autofunc.Result {
	return autofunc.Pool(a, func(a autofunc.Result) autofunc.Result {
		xVar := &autofunc.Variable{x}
		logA := autofunc.Log{}.Apply(a)
		oneMinusA := autofunc.AddScaler(autofunc.Scale(a, -1), 1)
		oneMinusX := autofunc.AddScaler(autofunc.Scale(xVar, -1), 1)
		log1A := autofunc.Log{}.Apply(oneMinusA)

		errorVec := autofunc.Add(autofunc.Mul(xVar, logA),
			autofunc.Mul(oneMinusX, log1A))
		return autofunc.Scale(autofunc.SumAll(errorVec), -1)
	})
}

func (_ CrossEntropyCost) CostR(v autofunc.RVector, x linalg.Vector,
	a autofunc.RResult) autofunc.RResult {
	return autofunc.PoolR(a, func(a autofunc.RResult) autofunc.RResult {
		xVar := autofunc.NewRVariable(&autofunc.Variable{x}, autofunc.RVector{})
		logA := autofunc.Log{}.ApplyR(v, a)
		oneMinusA := autofunc.AddScalerR(autofunc.ScaleR(a, -1), 1)
		oneMinusX := autofunc.AddScalerR(autofunc.ScaleR(xVar, -1), 1)
		log1A := autofunc.Log{}.ApplyR(v, oneMinusA)

		errorVec := autofunc.AddR(autofunc.MulR(xVar, logA),
			autofunc.MulR(oneMinusX, log1A))
		return autofunc.ScaleR(autofunc.SumAllR(errorVec), -1)
	})
}

// DotCost simply computes the negative of the dot
// product of the actual and expected vectors.
// This is equivalent to cross entropy cost when
// used in conjunction with a LogSoftmaxLayer.
type DotCost struct{}

func (_ DotCost) Cost(x linalg.Vector, a autofunc.Result) autofunc.Result {
	xVar := &autofunc.Variable{x}
	return autofunc.Scale(autofunc.SumAll(autofunc.Mul(xVar, a)), -1)
}

func (_ DotCost) CostR(v autofunc.RVector, x linalg.Vector,
	a autofunc.RResult) autofunc.RResult {
	xVar := autofunc.NewRVariable(&autofunc.Variable{x}, v)
	return autofunc.ScaleR(autofunc.SumAllR(autofunc.MulR(xVar, a)), -1)
}

// SigmoidCECost applies a sigmoid to the actual
// output and then uses cross-entropy loss on the
// result.
// This is more numerically stable than feeding the
// output of a sigmoid to a cross-entropy loss.
type SigmoidCECost struct{}

func (_ SigmoidCECost) Cost(x linalg.Vector, a autofunc.Result) autofunc.Result {
	logsig := autofunc.LogSigmoid{}
	log := logsig.Apply(a)
	invLog := logsig.Apply(autofunc.Scale(a, -1))

	xVar := &autofunc.Variable{x}
	oneMinusX := autofunc.AddScaler(autofunc.Scale(xVar, -1), 1)

	sums := autofunc.Add(autofunc.Mul(xVar, log), autofunc.Mul(oneMinusX, invLog))
	return autofunc.Scale(autofunc.SumAll(sums), -1)
}

func (_ SigmoidCECost) CostR(v autofunc.RVector, x linalg.Vector,
	a autofunc.RResult) autofunc.RResult {
	logsig := autofunc.LogSigmoid{}
	log := logsig.ApplyR(v, a)
	invLog := logsig.ApplyR(v, autofunc.ScaleR(a, -1))

	xVar := autofunc.NewRVariable(&autofunc.Variable{x}, v)
	oneMinusX := autofunc.AddScalerR(autofunc.ScaleR(xVar, -1), 1)

	sums := autofunc.AddR(autofunc.MulR(xVar, log), autofunc.MulR(oneMinusX, invLog))
	return autofunc.ScaleR(autofunc.SumAllR(sums), -1)
}

// RegularizingCost adds onto another cost function
// the squared magnitudes of various variables.
type RegularizingCost struct {
	Variables []*autofunc.Variable

	// Penalty is used as a coefficient for the
	// magnitudes of the regularized variables.
	Penalty float64

	CostFunc CostFunc
}

func (r *RegularizingCost) Cost(a linalg.Vector, x autofunc.Result) autofunc.Result {
	regFunc := autofunc.SquaredNorm{}
	cost := r.CostFunc.Cost(a, x)
	for _, variable := range r.Variables {
		norm := regFunc.Apply(variable)
		cost = autofunc.Add(cost, autofunc.Scale(norm, r.Penalty))
	}
	return cost
}

func (r *RegularizingCost) CostR(v autofunc.RVector, a linalg.Vector,
	x autofunc.RResult) autofunc.RResult {
	regFunc := autofunc.SquaredNorm{}
	cost := r.CostFunc.CostR(v, a, x)
	for _, variable := range r.Variables {
		norm := regFunc.ApplyR(v, autofunc.NewRVariable(variable, v))
		cost = autofunc.AddR(cost, autofunc.ScaleR(norm, r.Penalty))
	}
	return cost
}
